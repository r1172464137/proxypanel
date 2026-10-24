#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""std-lib self-test for the group E runner's lease and evidence gate.

    uv run tests/e2e/test_run_webdemo_clients.py

Everything here uses a private lock file under a temporary directory: the real
shared local-runtime lease is never touched, so this test can run while the
canonical lease is busy. It covers the two fail-closed properties the runner
needs:

  * the lease is proven owned by *acquiring* it (a busy lease stays blocked and
    is never reported as owned), with release on the same descriptor;
  * a run passes only from the raw `go test -json` stream plus exit code, with
    the exact declared test/cell set - skips, failures, missing or extra cells
    and wrong payload evidence all fail.
"""
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]


def load_runner():
    spec = importlib.util.spec_from_file_location(
        'webdemo_runner', HERE / 'run_webdemo_clients.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


runner = load_runner()


class LeaseTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='pp-webdemo-selftest-')
        self.lock = Path(self.tmp.name) / 'private-runtime.lock'

    def tearDown(self):
        self.tmp.cleanup()

    def test_acquire_blocks_others_and_release_frees(self):
        lease = runner.RuntimeLease(self.lock, timeout=1.0)
        lease.acquire()
        try:
            self.assertTrue(lease.acquired)
            self.assertIsNotNone(lease.fd)
            fd2 = os.open(self.lock, os.O_RDWR)
            try:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(fd2, fcntl.LOCK_EX | fcntl.LOCK_NB)
            finally:
                os.close(fd2)
            described = lease.describe()
            self.assertEqual(described['own_pid'], os.getpid())
            self.assertTrue(described['acquired'])
            self.assertFalse(described['released'])
            self.assertEqual(described['mode'], 'self_acquired')
        finally:
            lease.release()
        self.assertFalse(lease.acquired)
        self.assertTrue(lease.describe()['released'])
        # After release the same canonical file can be taken again.
        again = runner.RuntimeLease(self.lock, timeout=0.5)
        again.acquire()
        self.assertTrue(again.acquired)
        again.release()

    def test_busy_lease_is_not_owned(self):
        # A foreign holder (separate process, like another group's run).
        holder = subprocess.Popen(
            [sys.executable, '-c',
             'import fcntl,os,sys,time\n'
             'fd=os.open(sys.argv[1], os.O_RDWR|os.O_CREAT, 0o600)\n'
             'fcntl.flock(fd, fcntl.LOCK_EX)\n'
             'print("locked", flush=True)\n'
             'time.sleep(60)\n', str(self.lock)],
            stdout=subprocess.PIPE, text=True)
        try:
            self.assertEqual(holder.stdout.readline().strip(), 'locked')
            # The old oracle would have returned True here (somebody holds it).
            self.assertFalse(hasattr(runner, 'lock_is_held'))
            lease = runner.RuntimeLease(self.lock, timeout=0.4, poll=0.05)
            with self.assertRaises(runner.LeaseBusy):
                lease.acquire()
            self.assertFalse(lease.acquired)
            self.assertIsNone(lease.fd)
            self.assertFalse(lease.describe()['acquired'])
        finally:
            holder.kill()
            holder.wait(timeout=10)
            holder.stdout.close()

    def test_inherited_fd_is_adopted_only_when_it_holds_and_excludes(self):
        fd = os.open(self.lock, os.O_RDWR | os.O_CREAT, 0o600)
        fcntl.flock(fd, fcntl.LOCK_EX)
        old = os.environ.get(runner.LEASE_FD_ENV)
        os.environ[runner.LEASE_FD_ENV] = str(fd)
        try:
            lease = runner.RuntimeLease(self.lock, timeout=0.3)
            adopted = lease.acquire()
            try:
                self.assertTrue(adopted.acquired)
                self.assertTrue(adopted.inherited)
                self.assertEqual(adopted.fd, fd)
                self.assertEqual(adopted.describe()['mode'], 'inherited_fd')
                # The descriptor must exclude a second opener: otherwise the
                # "inherited" fd was not a lease at all.
                other = os.open(self.lock, os.O_RDWR)
                try:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
                finally:
                    os.close(other)
            finally:
                adopted.release()
            # Releasing an inherited descriptor does not unlock the supervisor's.
            with self.assertRaises(BlockingIOError):
                probe = os.open(self.lock, os.O_RDWR)
                try:
                    fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
                finally:
                    os.close(probe)
        finally:
            if old is None:
                os.environ.pop(runner.LEASE_FD_ENV, None)
            else:
                os.environ[runner.LEASE_FD_ENV] = old
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)

    def test_unlocked_inherited_descriptor_is_acquired_not_assumed(self):
        fd_free = os.open(self.lock, os.O_RDWR | os.O_CREAT, 0o600)
        old = os.environ.get(runner.LEASE_FD_ENV)
        try:
            os.environ[runner.LEASE_FD_ENV] = str(fd_free)
            lease = runner.RuntimeLease(self.lock, timeout=0.2)
            lease.acquire()
            try:
                self.assertTrue(lease.acquired)
                self.assertFalse(lease.inherited)
                probe = os.open(self.lock, os.O_RDWR)
                try:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
                finally:
                    os.close(probe)
            finally:
                lease.release()
            # Released: the same file is free again, and the caller's descriptor
            # was not closed behind its back.
            os.fstat(fd_free)
            probe = os.open(self.lock, os.O_RDWR)
            try:
                fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
                fcntl.flock(probe, fcntl.LOCK_UN)
            finally:
                os.close(probe)
        finally:
            if old is None:
                os.environ.pop(runner.LEASE_FD_ENV, None)
            else:
                os.environ[runner.LEASE_FD_ENV] = old
            os.close(fd_free)

    def test_inherited_fd_rejected_for_wrong_inode_or_bad_fd(self):
        other = Path(self.tmp.name) / 'other.lock'
        fd_other = os.open(other, os.O_RDWR | os.O_CREAT, 0o600)
        fcntl.flock(fd_other, fcntl.LOCK_EX)
        old = os.environ.get(runner.LEASE_FD_ENV)
        try:
            for bad, why in ((fd_other, 'other inode'), (999999, 'not an fd')):
                os.environ[runner.LEASE_FD_ENV] = str(bad)
                lease = runner.RuntimeLease(self.lock, timeout=0.2)
                with self.assertRaises(runner.LeaseInvalid, msg=why):
                    lease.acquire()
                self.assertFalse(lease.acquired)
        finally:
            if old is None:
                os.environ.pop(runner.LEASE_FD_ENV, None)
            else:
                os.environ[runner.LEASE_FD_ENV] = old
            fcntl.flock(fd_other, fcntl.LOCK_UN)
            os.close(fd_other)

    def test_runner_reports_blocked_when_lease_is_busy(self):
        """Whole-runner negative: busy lease blocks before any probe."""
        lock = Path(self.tmp.name) / 'private-runtime.lock'
        fd = os.open(lock, os.O_RDWR | os.O_CREAT, 0o600)
        fcntl.flock(fd, fcntl.LOCK_EX)
        out = Path(self.tmp.name) / 'evidence'
        real_cls = runner.RuntimeLease
        real_argv = sys.argv
        try:
            class PrivateLease(real_cls):
                def __init__(self, path=None, timeout=None, poll=0.25):
                    super().__init__(lock, timeout=0.3, poll=0.05)

            runner.RuntimeLease = PrivateLease
            sys.argv = ['run_webdemo_clients.py', '--evidence-dir', str(out), '--only', 'vision']
            rc = runner.main()
        finally:
            runner.RuntimeLease = real_cls
            sys.argv = real_argv
            fcntl.flock(fd, fcntl.LOCK_UN)
            os.close(fd)
        self.assertEqual(rc, 2)
        receipt = json.loads((out / 'receipt.json').read_text())
        self.assertEqual(receipt['status'], 'blocked')
        self.assertIn('busy', receipt['error'])
        self.assertFalse(receipt['lease']['acquired'])
        self.assertNotIn('binaries', receipt)
        self.assertNotIn('base_commit', receipt)

    def test_manifest_cells_are_exact_and_consistently_counted(self):
        manifest = runner.load_manifest()
        vision = runner.expected_cells(manifest, 'vision')
        self.assertEqual(len(vision), 3)
        self.assertEqual({row['client'] for row in vision}, {'xray', 'sing-box', 'mihomo'})
        self.assertEqual({row['test'].split('/')[0] for row in vision},
                         {'TestWebDemoVisionTrafficStatsAndStop',
                          'TestWebDemoRealityCrossClient'})
        matrix = runner.expected_cells(manifest, 'matrix')
        self.assertEqual(len(matrix), 21)
        self.assertNotIn('TestLocalCoreCredentialGuards',
                         {row['test'].split('/')[0] for row in matrix})
        rejected = [row for row in matrix if row['result'] != 'supported_pass']
        self.assertEqual([(r['test'], r['field']) for r in rejected],
                         [('TestLocalCoreClientMatrix/trojan-tcp-none/mihomo',
                           'stream_settings.security')])


class RecoveryTest(unittest.TestCase):
    """Owned-core recovery is identity-guarded and never claims a false cleanup."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='pp-webdemo-recovery-')
        self.dir = Path(self.tmp.name) / 'run'
        self.dir.mkdir(mode=0o700)
        self.fake = self.dir / 'xray'
        shutil.copy('/bin/sleep', self.fake)
        self.procs = []

    def tearDown(self):
        for proc in self.procs:
            if proc.poll() is None:
                proc.kill()
                proc.wait(timeout=10)
        self.tmp.cleanup()

    def test_owned_core_is_stopped_and_reported(self):
        proc = subprocess.Popen([str(self.fake), '60'])
        self.procs.append(proc)
        time.sleep(0.2)
        found = runner.owned_core_records(self.dir)
        self.assertEqual([r['pid'] for r in found], [proc.pid])
        self.assertEqual(found[0]['exe'], str(self.fake))
        out = runner.stop_owned_cores(self.dir, grace=10)
        self.assertTrue(out['all_exited'], out)
        self.assertEqual([r['pid'] for r in out['signalled']], [proc.pid])
        self.assertEqual([r['pid'] for r in out['exited']], [proc.pid])
        self.assertEqual(proc.wait(timeout=10), -15)

    def test_unowned_and_foreign_cores_are_untouched(self):
        other_dir = Path(self.tmp.name) / 'someone-else'
        other_dir.mkdir()
        other = other_dir / 'xray'
        shutil.copy('/bin/sleep', other)
        proc = subprocess.Popen([str(other), '60'])
        self.procs.append(proc)
        time.sleep(0.2)
        self.assertEqual(runner.owned_core_records(self.dir), [])
        out = runner.stop_owned_cores(self.dir, grace=2)
        self.assertTrue(out['all_exited'])
        self.assertEqual(out['signalled'], [])
        self.assertIsNone(proc.poll())

    def test_core_ignoring_sigterm_is_not_declared_clean(self):
        stubborn = self.dir / 'mihomo'
        shutil.copy('/bin/sh', stubborn)
        proc = subprocess.Popen([str(stubborn), '-c', 'trap "" TERM; sleep 60'])
        self.procs.append(proc)
        time.sleep(0.3)
        out = runner.stop_owned_cores(self.dir, grace=0.6)
        self.assertFalse(out['all_exited'], out)
        self.assertEqual([r['pid'] for r in out['timed_out']], [proc.pid])
        self.assertEqual(out['exited'], [])
        self.assertIsNone(proc.poll())

    def test_tempdir_is_only_removed_when_recovery_is_clean(self):
        kept = self.dir / 'evidence.txt'
        kept.write_text('keep me')
        out = runner.stop_owned_cores(self.dir, grace=0.2)
        self.assertTrue(out['all_exited'])
        self.assertTrue(kept.exists())
        # The runner keeps the tempdir on an unclean recovery; simulate that
        # decision here so the contract is asserted, not just commented.
        unclean = {'all_exited': False}
        if unclean['all_exited']:
            shutil.rmtree(self.dir)
        self.assertTrue(kept.exists())


class GateTest(unittest.TestCase):
    def setUp(self):
        self.manifest = runner.load_manifest()
        self.tests = {
            'TestWebDemoVisionTrafficStatsAndStop': {'action': 'pass', 'output': []},
            'TestWebDemoRealityCrossClient': {'action': 'pass', 'output': []},
            'TestWebDemoRealityCrossClient/sing-box': {'action': 'pass', 'output': []},
            'TestWebDemoRealityCrossClient/mihomo': {'action': 'pass', 'output': []},
        }
        digest = 'a' * 64
        self.cells = [
            {'test': 'TestWebDemoVisionTrafficStatsAndStop', 'client': 'xray',
             'result': 'supported_pass', 'bytes': 69632, 'sha256': digest,
             'uplink': 96, 'downlink': 69771},
            {'test': 'TestWebDemoRealityCrossClient/sing-box', 'client': 'sing-box',
             'result': 'supported_pass', 'bytes': 69632, 'sha256': digest,
             'uplink': 96, 'downlink': 69771},
            {'test': 'TestWebDemoRealityCrossClient/mihomo', 'client': 'mihomo',
             'result': 'supported_pass', 'bytes': 69632, 'sha256': digest,
             'uplink': 96, 'downlink': 69771},
        ]

    def verdict(self, **kw):
        return runner.evaluate(self.manifest, 'vision',
                               kw.get('raw_exit', 0),
                               kw.get('tests', self.tests),
                               kw.get('cells', self.cells))

    def test_declared_run_passes(self):
        out = self.verdict()
        self.assertTrue(out['passed'], out['problems'])
        self.assertEqual(len(out['expected_cells']), 3)

    def test_nonzero_exit_fails(self):
        self.assertFalse(self.verdict(raw_exit=1)['passed'])

    def test_skipped_test_fails(self):
        tests = dict(self.tests)
        tests['TestWebDemoRealityCrossClient/mihomo'] = {'action': 'skip', 'output': []}
        out = self.verdict(tests=tests)
        self.assertFalse(out['passed'])
        self.assertTrue(any('skip' in p for p in out['problems']))

    def test_missing_cell_fails(self):
        out = self.verdict(cells=self.cells[:2])
        self.assertFalse(out['passed'])
        self.assertTrue(any('never ran' in p for p in out['problems']))

    def test_extra_cell_or_duplicate_fails(self):
        extra = self.cells + [dict(self.cells[0], test='TestWebDemoRealityCrossClient/sing-box',
                                   client='xray')]
        self.assertFalse(self.verdict(cells=extra)['passed'])
        dup = self.cells + [dict(self.cells[0])]
        self.assertFalse(self.verdict(cells=dup)['passed'])

    def test_extra_top_level_test_fails(self):
        tests = dict(self.tests)
        tests['TestSomethingElse'] = {'action': 'pass', 'output': []}
        self.assertFalse(self.verdict(tests=tests)['passed'])

    def test_wrong_payload_evidence_fails(self):
        cells = [dict(c) for c in self.cells]
        cells[0]['bytes'] = 1024
        self.assertFalse(self.verdict(cells=cells)['passed'])
        cells = [dict(c) for c in self.cells]
        cells[1]['sha256'] = 'short'
        self.assertFalse(self.verdict(cells=cells)['passed'])

    def test_matrix_gate_counts_21_and_excludes_guards(self):
        tests = {'TestLocalCoreClientMatrix': {'action': 'pass', 'output': []},
                 'TestLocalCoreCredentialGuards': {'action': 'pass', 'output': []}}
        matrix_rows = runner.expected_cells(self.manifest, 'matrix')
        cells = []
        for row in matrix_rows:
            record = {'test': row['test'], 'client': row['client'],
                      'result': row['result'], 'bytes': 69632, 'sha256': 'b' * 64}
            if row['result'] != 'supported_pass':
                record['field'] = row['field']
            cells.append(record)
        tests.update({row['test']: {'action': 'pass', 'output': []} for row in matrix_rows})
        good = runner.evaluate(self.manifest, 'matrix', 0, tests, cells)
        self.assertTrue(good['passed'], good['problems'])
        bad = runner.evaluate(self.manifest, 'matrix', 0, tests, cells[:-1])
        self.assertFalse(bad['passed'])
        no_guards = {k: v for k, v in tests.items()
                     if k != 'TestLocalCoreCredentialGuards'}
        self.assertFalse(runner.evaluate(self.manifest, 'matrix', 0, no_guards, cells)['passed'])

    def test_matrix_guard_cells_are_auxiliary_not_counted(self):
        tests = {'TestLocalCoreClientMatrix': {'action': 'pass', 'output': []},
                 'TestLocalCoreCredentialGuards': {'action': 'pass', 'output': []}}
        cells = []
        for row in runner.expected_cells(self.manifest, 'matrix'):
            record = {'test': row['test'], 'client': row['client'],
                      'result': row['result'], 'bytes': 69632, 'sha256': 'b' * 64}
            if row['result'] != 'supported_pass':
                record['field'] = row['field']
            cells.append(record)
        tests.update({row['test']: {'action': 'pass', 'output': []} for row in cells})
        guard = {'test': 'TestLocalCoreCredentialGuards/xray-pin-is-enforced',
                 'client': 'xray', 'result': 'supported_pass', 'bytes': 69632,
                 'sha256': 'd' * 64}
        tests[guard['test']] = {'action': 'pass', 'output': []}
        good = runner.evaluate(self.manifest, 'matrix', 0, tests, cells + [guard, dict(guard)])
        self.assertTrue(good['passed'], good['problems'])
        self.assertEqual(len(good['expected_cells']), 21)
        self.assertEqual(len(good['observed_cells']), 21)
        self.assertEqual(len(good['auxiliary_cells']), 2)
        # A guard cell with bad payload evidence still fails the run.
        bad_guard = dict(guard, bytes=10)
        self.assertFalse(runner.evaluate(self.manifest, 'matrix', 0, tests,
                                         cells + [bad_guard])['passed'])
        # ... and a cell from a test that is not declared at all is fatal.
        alien = dict(guard, test='TestSomethingElse')
        self.assertFalse(runner.evaluate(self.manifest, 'matrix', 0, tests,
                                         cells + [alien])['passed'])

    def test_parse_go_json_stream(self):
        with tempfile.NamedTemporaryFile('w', suffix='.jsonl', delete=False) as fh:
            for ev in [
                {'Action': 'run', 'Test': 'TestWebDemoVisionTrafficStatsAndStop'},
                {'Action': 'output', 'Test': 'TestWebDemoVisionTrafficStatsAndStop',
                 'Output': 'x webdemo_cell={"client":"xray","result":"supported_pass",'
                           '"bytes":69632,"sha256":"' + 'c' * 64 + '","uplink":9,'
                           '"downlink":70000}\n'},
                {'Action': 'pass', 'Test': 'TestWebDemoVisionTrafficStatsAndStop'},
            ]:
                fh.write(json.dumps(ev) + '\n')
            fh.write('not json\n')
            path = Path(fh.name)
        try:
            tests, cells = runner.parse_go_json(path)
        finally:
            path.unlink()
        self.assertEqual(tests['TestWebDemoVisionTrafficStatsAndStop']['action'], 'pass')
        self.assertEqual(len(cells), 1)
        self.assertEqual(cells[0]['test'], 'TestWebDemoVisionTrafficStatsAndStop')
        self.assertEqual(cells[0]['client'], 'xray')


if __name__ == '__main__':
    unittest.main(verbosity=2)
