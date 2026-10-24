#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""web-demo group E: owned real-client compatibility runner.

    uv run tests/e2e/run_webdemo_clients.py [--evidence-dir ABS]
                                            [--only vision|matrix] [--keep-temp]

The runner itself takes the canonical local-runtime lease (an exclusive flock on
the shared lock file) *before* it probes a single core version or snapshots the
source tree, and holds it until recovery/cleanup is finished. No outer flock is
needed and none is accepted as proof: holding the lease is established by
acquiring it, never by observing that somebody else holds it. A busy lease is a
bounded wait followed by a specific, fail-closed receipt.

Passing is judged from the raw `go test -json` stream and the process exit code:
the exact expected test names and client cells declared in
tests/e2e/webdemo_clients_manifest.json must all run, pass with the declared
result, and carry the declared payload evidence. Skips, failures, missing cells
or extra/absent top-level tests can never produce a pass.

Only this group's own selections run, against the pinned official cores, on
loopback. No existing panel, Agent lab, preview stack, database or pinned binary
is touched.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
MAIN = Path('/home/compile/Work/proxypanel')
LOCK = Path('/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock')
DEFAULT_EVIDENCE = MAIN / '.ai/evidence/web-demo/E'
MANIFEST = ROOT / 'tests/e2e/webdemo_clients_manifest.json'

# Inheritance hook: a supervisor that already acquired the canonical lease may
# pass its open and locked descriptor. It is adopted only after the descriptor is
# proven to be the same inode and to exclude every other holder.
LEASE_FD_ENV = 'WEBDEMO_LOCAL_RUNTIME_LOCK_FD'
LEASE_TIMEOUT_ENV = 'WEBDEMO_LOCAL_RUNTIME_LEASE_TIMEOUT'
DEFAULT_LEASE_TIMEOUT = 300.0

CORE_NAMES = {'xray', 'sing-box', 'mihomo'}
# Same pinned payload constant as tests/e2e/webdemo_clients_vision_test.go and
# internal/panel/clientconfig/realcore_test.go (localPayload).
EXPECTED_PAYLOAD_BYTES = 69632


class LeaseBusy(RuntimeError):
    pass


class LeaseInvalid(RuntimeError):
    pass


class RuntimeLease:
    """Canonical local-runtime lease held by this very process."""

    def __init__(self, path=LOCK, timeout=None, poll=0.25):
        self.path = Path(path)
        if timeout is None:
            timeout = os.environ.get(LEASE_TIMEOUT_ENV, DEFAULT_LEASE_TIMEOUT)
        self.timeout = float(timeout)
        self.poll = poll
        self.fd = None
        self.own_fd = True
        self.acquired = False
        self.inherited = False
        self.acquired_at = None
        self.released_at = None

    def _open(self):
        self.path.parent.mkdir(parents=True, exist_ok=True)
        return os.open(self.path, os.O_RDWR | os.O_CREAT | os.O_CLOEXEC, 0o600)

    def acquire(self):
        inherited = os.environ.get(LEASE_FD_ENV, '').strip()
        if inherited:
            self._adopt(int(inherited))
            return self
        fd = self._open()
        deadline = time.monotonic() + self.timeout
        while True:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    os.close(fd)
                    raise LeaseBusy(
                        f'shared local-runtime lease {self.path} stayed busy for '
                        f'{self.timeout:g}s; nothing was probed and nothing ran '
                        f'(raise {LEASE_TIMEOUT_ENV} to wait longer)')
                time.sleep(self.poll)
        self.fd = fd
        self.acquired = True
        self.acquired_at = iso_now()
        return self

    def _adopt(self, fd):
        try:
            st_fd = os.fstat(fd)
        except OSError as exc:
            raise LeaseInvalid(f'{LEASE_FD_ENV}={fd} is not an open descriptor: {exc}')
        try:
            st_path = os.stat(self.path)
        except OSError as exc:
            raise LeaseInvalid(f'{LEASE_FD_ENV}={fd}: lease file {self.path}: {exc}')
        if (st_fd.st_dev, st_fd.st_ino) != (st_path.st_dev, st_path.st_ino):
            raise LeaseInvalid(f'{LEASE_FD_ENV}={fd} is not the canonical lease {self.path}')
        # A second open of the same file is a different open-file-description, so
        # it conflicts with an exclusive lock on any descriptor. That is the only
        # way to tell "this descriptor already holds the lease" from "the file
        # happens to be free": re-flocking our own descriptor always succeeds.
        probe = self._open()
        try:
            fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self.inherited = True
        else:
            fcntl.flock(probe, fcntl.LOCK_UN)
            self.inherited = False
            # Free file: lock the inherited descriptor itself so the caller sees
            # the lease where it asked for it, and release it again later.
            fcntl.flock(fd, fcntl.LOCK_EX)
        finally:
            os.close(probe)
        self.fd = fd
        self.own_fd = False
        self.acquired = True
        self.acquired_at = iso_now()
        return self

    def release(self):
        if not self.acquired:
            return
        self.acquired = False
        try:
            # An inherited descriptor stays locked by the supervisor that owns it;
            # a lease we took on an inherited descriptor is unlocked but the
            # descriptor itself (owned by the caller) is left open.
            if not self.inherited:
                fcntl.flock(self.fd, fcntl.LOCK_UN)
            if self.own_fd:
                os.close(self.fd)
            self.fd = None
        finally:
            self.released_at = iso_now()

    def describe(self):
        return {
            'path': str(self.path),
            'mode': 'inherited_fd' if self.inherited else 'self_acquired',
            'own_pid': os.getpid(),
            'fd': self.fd,
            'acquired': self.acquired,
            'acquired_at': self.acquired_at,
            'released': self.released_at is not None,
            'released_at': self.released_at,
            'bounded_wait_s': self.timeout,
        }


def iso_now():
    return time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def find_go():
    return Path(os.environ.get('GO_BIN', '/home/compile/sdk/go1.27.3/go/bin/go'))


def cores():
    return {
        'XRAY_BIN': Path(os.environ.get('XRAY_BIN', '/home/compile/Work/bin/xray')),
        'SING_BOX_BIN': Path(os.environ.get('SING_BOX_BIN', '/home/compile/Work/bin/sing-box')),
        'MIHOMO_BIN': Path(os.environ.get('MIHOMO_BIN', '/home/compile/Work/bin/mihomo')),
    }


def version_args(name):
    if name == 'XRAY_BIN':
        return ['version']
    if name == 'MIHOMO_BIN':
        return ['-v']
    return ['version']


def version_of(binary, args):
    out = subprocess.run([str(binary), *args], capture_output=True, text=True, check=True,
                         timeout=120)
    return out.stdout.strip().splitlines()[0] if out.stdout.strip() else ''


def load_manifest():
    return json.loads(MANIFEST.read_text())


def selection(manifest, key):
    wanted = {'vision': 'webdemo-vision', 'matrix': 'webdemo-local-matrix'}[key]
    for entry in manifest['selections']:
        if entry['id'] == wanted:
            return entry
    raise SystemExit(f'manifest {MANIFEST} has no selection {wanted}')


def expected_cells(manifest, key):
    """Exact (test path, client, result) set declared for one selection."""
    sel = selection(manifest, key)
    if key == 'vision':
        rows = list(sel['expected_cells'])
    else:
        matrix = sel['matrix_cells']
        rejections = matrix.get('declared_rejections', {})
        rows = []
        for case in matrix['cases']:
            for client in matrix['clients']:
                declared = rejections.get(case, {}).get(client)
                row = {
                    'test': f'{matrix["subject_test"]}/{case}/{client}',
                    'client': client,
                    'result': 'declared_incompatible_rejected' if declared
                              else 'supported_pass',
                }
                if declared:
                    row['field'] = declared['field']
                rows.append(row)
        if len(rows) != matrix['expected_cells']:
            raise SystemExit(
                f'manifest matrix declares {matrix["expected_cells"]} cells but '
                f'{len(rows)} are generated by cases x clients')
    return rows


def cell_owner_tests(manifest, key):
    """Top-level tests whose cell records are part of the declared cell set."""
    sel = selection(manifest, key)
    if key == 'vision':
        return {row['test'].split('/')[0] for row in sel['expected_cells']}
    return {sel['matrix_cells']['subject_test']}


def parse_go_json(path):
    """Return (test states, cell records) from a raw `go test -json` stream."""
    tests = {}
    cells = []
    if not path.exists():
        return tests, cells
    for raw in path.read_text(errors='replace').splitlines():
        raw = raw.strip()
        if not raw.startswith('{'):
            continue
        try:
            ev = json.loads(raw)
        except json.JSONDecodeError:
            continue
        action, name = ev.get('Action'), ev.get('Test')
        if name:
            state = tests.setdefault(name, {'action': None, 'output': []})
            if action == 'run':
                state['action'] = 'running'
            elif action in {'pass', 'fail', 'skip'}:
                state['action'] = action
            elif action == 'output':
                line = ev.get('Output', '')
                state['output'].append(line)
                for marker in ('webdemo_cell=', 'matrix_cell='):
                    if marker in line:
                        payload = line.split(marker, 1)[1].strip()
                        try:
                            record = json.loads(payload)
                        except json.JSONDecodeError:
                            cells.append({'test': name, 'malformed': payload})
                            break
                        record['test'] = name
                        record['marker'] = marker[:-1]
                        cells.append(record)
                        break
    return tests, cells


def _supported_record_ok(record):
    if record.get('bytes') != EXPECTED_PAYLOAD_BYTES:
        return f'payload bytes {record.get("bytes")!r} != {EXPECTED_PAYLOAD_BYTES}'
    digest = str(record.get('sha256', ''))
    if len(digest) != 64 or any(c not in '0123456789abcdef' for c in digest):
        return f'sha256 {digest!r} is not a full digest'
    return None


def evaluate(manifest, key, raw_exit, tests, cells):
    """Fail closed unless every declared test and cell ran and passed."""
    problems = []
    want_rows = expected_cells(manifest, key)
    want_tests = list(selection(manifest, key)['expected_tests'])
    owner_tests = cell_owner_tests(manifest, key)
    try:
        tests = dict(tests)
        raw_exit = int(raw_exit)
        want_rows = [dict(r) for r in want_rows]
    except Exception as exc:  # noqa: BLE001 - any shape problem is a failure
        return {'passed': False, 'problems': [f'bad evidence shape: {exc}'], 'cells': []}

    if raw_exit != 0:
        problems.append(f'raw go test exit code {raw_exit} != 0')
    for name in want_tests:
        state = tests.get(name)
        if state is None:
            problems.append(f'expected test {name} did not run')
        elif state['action'] == 'skip':
            problems.append(f'expected test {name} was skipped')
        elif state['action'] != 'pass':
            problems.append(f'expected test {name} finished as {state["action"]!r}')
    for name, state in sorted(tests.items()):
        if state['action'] == 'skip':
            problems.append(f'test {name} was skipped; skips are not evidence')
        elif state['action'] == 'fail':
            problems.append(f'test {name} failed')
        elif state['action'] in (None, 'running'):
            problems.append(f'test {name} started but never finished')
    ran_top = {name.split('/')[0] for name in tests}
    want_top = {name.split('/')[0] for name in want_tests}
    if ran_top != want_top:
        problems.append(
            f'top-level tests ran {sorted(ran_top)}; declared exactly {sorted(want_top)}')

    seen = {}
    auxiliary = []
    for record in cells:
        if 'malformed' in record:
            problems.append(f'test {record["test"]} emitted a malformed cell record')
            continue
        top = str(record.get('test', '')).split('/')[0]
        if top not in owner_tests:
            # A guard/control subtest may log its own traffic cells. They are real
            # evidence of that test, but they are not part of the declared cell
            # set (e.g. the 21-cell main matrix), so they are reported apart.
            if top not in {t.split('/')[0] for t in want_tests}:
                problems.append(f'cell record from undeclared test {record.get("test")!r}')
                continue
            if record.get('result') != 'supported_pass':
                problems.append(f'auxiliary cell {record.get("test")!r}: '
                                f'unexpected result {record.get("result")!r}')
                continue
            bad = _supported_record_ok(record)
            if bad:
                problems.append(f'auxiliary cell {record.get("test")!r}: {bad}')
                continue
            auxiliary.append(record)
            continue
        pair = (record.get('test'), record.get('client'))
        if pair in seen:
            problems.append(f'duplicate cell {pair} (two records for one cell)')
        seen[pair] = record
    want_pairs = {(row['test'], row['client']): row for row in want_rows}
    missing = sorted(set(want_pairs) - set(seen))
    extra = sorted(set(seen) - set(want_pairs))
    if missing:
        problems.append(f'declared cells never ran: {missing}')
    if extra:
        problems.append(f'unexpected cells observed: {extra}')
    if len(seen) != len(want_rows):
        problems.append(f'distinct declared cells {len(seen)} != {len(want_rows)}')
    declared_owner_cells = sum(1 for r in cells
                               if str(r.get('test', '')).split('/')[0] in owner_tests)
    if declared_owner_cells != len(want_rows):
        problems.append(
            f'cell records {declared_owner_cells} from declared cells != {len(want_rows)}')
    for pair, want in sorted(want_pairs.items()):
        record = seen.get(pair)
        if record is None:
            continue
        if record.get('result') != want['result']:
            problems.append(f'cell {pair} result {record.get("result")!r} != {want["result"]!r}')
            continue
        if want['result'] == 'supported_pass':
            bad = _supported_record_ok(record)
            if bad:
                problems.append(f'cell {pair}: {bad}')
            if key == 'vision':
                if int(record.get('uplink', 0)) <= 0:
                    problems.append(f'cell {pair}: uplink counter did not advance')
                if int(record.get('downlink', 0)) < EXPECTED_PAYLOAD_BYTES:
                    problems.append(f'cell {pair}: downlink below payload size')
        elif record.get('field') != want.get('field'):
            problems.append(f'cell {pair}: rejection field {record.get("field")!r} '
                            f'!= {want.get("field")!r}')
    return {
        'passed': not problems,
        'problems': problems,
        'expected_tests': want_tests,
        'expected_cells': want_rows,
        'observed_cells': list(seen.values()),
        'auxiliary_cells': auxiliary,
        'raw_exit_code': raw_exit,
    }


def proc_starttime(pid):
    try:
        raw = Path(f'/proc/{pid}/stat').read_text()
        return raw.rsplit(')', 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def owned_core_records(tmp):
    """Cores whose own command line names a path inside this run's tempdir."""
    records = []
    prefix = str(tmp) + os.sep
    for entry in Path('/proc').glob('[0-9]*'):
        pid = int(entry.name)
        try:
            cmdline = (entry / 'cmdline').read_bytes().split(b'\0')
            exe = os.readlink(entry / 'exe')
        except OSError:
            continue
        if Path(exe).name not in CORE_NAMES:
            continue
        args = [part.decode('utf-8', 'replace') for part in cmdline if part]
        if not any(arg == str(tmp) or arg.startswith(prefix) for arg in args):
            continue
        records.append({
            'pid': pid,
            'exe': exe,
            'starttime': proc_starttime(pid),
            'args': args[:4],
        })
    return records


def _wait_pidfd(pidfd, deadline):
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return False
        readable, _, _ = select.select([pidfd], [], [], min(remaining, 1.0))
        if readable:
            return True


def stop_owned_cores(tmp, grace=20.0):
    """Stop only this run's cores and prove they are gone (pidfd + starttime)."""
    result = {'signalled': [], 'exited': [], 'timed_out': [], 'identity_changed': []}
    for record in owned_core_records(tmp):
        pid = record['pid']
        if proc_starttime(pid) != record['starttime']:
            result['identity_changed'].append(record)
            continue
        pidfd = None
        if hasattr(os, 'pidfd_open'):
            try:
                pidfd = os.pidfd_open(pid)
            except OSError:
                pidfd = None
        # Re-check identity after the descriptor is pinned: a pidfd plus an
        # unchanged starttime cannot belong to a recycled PID.
        if pidfd is not None and proc_starttime(pid) != record['starttime']:
            os.close(pidfd)
            result['identity_changed'].append(record)
            continue
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            if pidfd is not None:
                os.close(pidfd)
            result['exited'].append(record)
            continue
        result['signalled'].append(record)
        deadline = time.monotonic() + grace
        if pidfd is not None:
            exited = _wait_pidfd(pidfd, deadline)
            os.close(pidfd)
        else:
            exited = False
            while time.monotonic() < deadline:
                if proc_starttime(pid) != record['starttime']:
                    exited = True
                    break
                time.sleep(0.2)
        (result['exited'] if exited else result['timed_out']).append(record)
    result['all_exited'] = not result['timed_out'] and not result['identity_changed']
    return result


def write_receipt(out, receipt):
    path = out / 'receipt.json'
    path.write_text(json.dumps(receipt, indent=2) + '\n')
    os.chmod(path, 0o600)


def run_selection(key, spec, manifest, env, out, go):
    log = out / f'{key}.jsonl'
    cmd = [str(go), 'test', '-count=1', '-p=1', '-timeout=600s', '-json', '-run', spec['run']]
    for tag in spec.get('build_tags', []):
        cmd += ['-tags', tag]
    cmd += [spec['package']]
    raw_exit = None
    error = None
    try:
        with log.open('wb') as fh:
            proc = subprocess.run(cmd, cwd=ROOT, env=env, stdout=fh,
                                  stderr=subprocess.STDOUT, timeout=900)
        raw_exit = proc.returncode
    except subprocess.TimeoutExpired:
        raw_exit = 'timeout'
        error = 'go test exceeded the 900s wrapper bound'
    ref = {'command': cmd, 'raw_exit': raw_exit, 'raw_json_log': str(log)}
    if error:
        ref.update({'passed': False, 'problems': [error]})
        return ref
    tests, cells = parse_go_json(log)
    verdict = evaluate(manifest, key, raw_exit, tests, cells)
    ref.update(verdict)
    ref['observed_tests'] = {
        name: {'action': state['action']} for name, state in sorted(tests.items())}
    return ref


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--evidence-dir', type=Path)
    ap.add_argument('--only', choices=['vision', 'matrix'], action='append')
    ap.add_argument('--keep-temp', action='store_true')
    args = ap.parse_args()

    run_id = time.strftime('%Y%m%dT%H%M%SZ') + '-' + uuid.uuid4().hex[:8]
    try:
        manifest = load_manifest()
    except (OSError, ValueError) as exc:
        print(f'cannot read {MANIFEST}: {exc}', file=sys.stderr)
        return 2
    out = args.evidence_dir or DEFAULT_EVIDENCE / run_id
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.umask(0o077)
    receipt = {
        'schema': 'webdemo-clients-evidence/v2',
        'run_id': run_id,
        'group': 'E',
        'status': 'blocked',
        'mode': 'owned loopback real cores; no existing panel/Agent/preview/database touched',
        'source_root': str(ROOT),
        'manifest': str(MANIFEST),
        'manifest_sha256': sha(MANIFEST),
        'lease': {'path': str(LOCK), 'acquired': False},
        'tempdir_removed': False,
    }
    lease = RuntimeLease(LOCK)
    selected = args.only or ['vision', 'matrix']
    tmp = None
    try:
        # The lease is taken before anything is probed, snapshotted or executed.
        try:
            lease.acquire()
        except (LeaseBusy, LeaseInvalid) as exc:
            receipt['status'] = 'blocked'
            receipt['error'] = str(exc)
            receipt['lease'] = lease.describe()
            write_receipt(out, receipt)
            print(json.dumps(receipt, indent=2))
            return 2
        receipt['lease'] = lease.describe()

        go = find_go()
        binary_paths = cores()
        missing = [f'{name}={path}' for name, path in [('go', go), *binary_paths.items()]
                   if not Path(path).is_file()]
        if missing:
            receipt['status'] = 'blocked'
            receipt['error'] = 'missing tool(s): ' + ', '.join(missing)
            write_receipt(out, receipt)
            print(json.dumps(receipt, indent=2))
            return 2

        receipt['base_commit'] = subprocess.check_output(
            ['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        receipt['git_status'] = subprocess.check_output(
            ['git', 'status', '--porcelain'], cwd=ROOT, text=True).strip().splitlines()
        receipt['binaries'] = {
            str(path): {'sha256': sha(path), 'version': version_of(path, version_args(name))}
            for name, path in binary_paths.items()
        }
        receipt['source_files'] = {
            str(p.relative_to(ROOT)): sha(p)
            for p in [
                ROOT / 'tests/e2e/webdemo_clients_vision_test.go',
                ROOT / 'tests/e2e/run_webdemo_clients.py',
                ROOT / 'tests/e2e/webdemo_clients_manifest.json',
                ROOT / 'tests/e2e/test_run_webdemo_clients.py',
                ROOT / 'internal/panel/clientconfig/realcore_test.go',
                ROOT / 'internal/panel/clientconfig/realcore_guard_test.go',
                ROOT / 'internal/panel/clientconfig/clients.go',
                ROOT / 'internal/panel/clientconfig/clientconfig.go',
                ROOT / 'internal/panel/protocol/endpoint.go',
                ROOT / 'internal/panel/protocol/protocol.go',
                ROOT / 'internal/panel/xrayconf/xrayconf.go',
                ROOT / 'go.mod',
                ROOT / 'go.sum',
            ] if p.exists()
        }

        tmp = Path(tempfile.mkdtemp(prefix='pp-webdemo-clients-'))
        receipt['tempdir'] = str(tmp)
        env = dict(os.environ)
        env['PATH'] = str(go.parent) + ':' + env['PATH']
        env['GOTOOLCHAIN'] = 'local'
        for key in list(env):
            if (key.startswith('PANEL_TEST_') or key.startswith('PANEL_BROWSER_')
                    or key.startswith('PANEL_DOCKER')):
                env.pop(key)
        for name, path in binary_paths.items():
            env[name] = str(path)
        # This stage pins Mihomo 1.19.33 explicitly; the historical helper default
        # is 1.19.32. The binaries themselves are not modified.
        env.setdefault('MIHOMO_BIN_VERSION', '1.19.33')
        receipt['pins'] = {
            'XRAY_BIN_VERSION': env.get('XRAY_BIN_VERSION', '26.3.27 (helper default)'),
            'SING_BOX_BIN_VERSION': env.get('SING_BOX_BIN_VERSION', '1.14.3 (helper default)'),
            'MIHOMO_BIN_VERSION': env['MIHOMO_BIN_VERSION'],
        }
        env['TMPDIR'] = str(tmp / 'work')
        (tmp / 'work').mkdir(mode=0o700)
        receipt['env_scrubbed'] = 'PANEL_TEST_/PANEL_BROWSER_/PANEL_DOCKER_* not inherited'

        results = {}
        status = 'pass'
        for key in selected:
            results[key] = run_selection(key, selection(manifest, key), manifest, env, out, go)
            if not results[key]['passed']:
                status = 'fail'
                break
        receipt['results'] = results
        receipt['status'] = status
    except Exception as exc:  # noqa: BLE001 - the receipt must record the blocker
        receipt['status'] = 'fail'
        receipt['error'] = f'{type(exc).__name__}: {exc}'
    finally:
        # Recovery is identity-guarded and evidence-preserving: a core that does
        # not leave within the grace period keeps the tempdir and fails the run.
        if tmp is not None:
            recovery = stop_owned_cores(tmp)
            receipt['recovery'] = recovery
            if not recovery['all_exited']:
                receipt['status'] = 'fail'
                receipt['tempdir_retained_for_failure'] = str(tmp)
                receipt['error'] = (
                    'owned core did not exit or changed identity during recovery '
                    f'(timed_out={recovery["timed_out"]}, '
                    f'identity_changed={recovery["identity_changed"]}); '
                    'tempdir kept as evidence')
            elif args.keep_temp:
                receipt['tempdir_kept'] = str(tmp)
            else:
                shutil.rmtree(tmp, ignore_errors=True)
                receipt['tempdir_removed'] = not tmp.exists()
        lease.release()
        receipt['lease'] = lease.describe()
        receipt['finished_at'] = iso_now()
        write_receipt(out, receipt)
        summary = {k: receipt[k] for k in ('run_id', 'status', 'results', 'error', 'lease')
                   if k in receipt}
        print(json.dumps(summary, indent=2))
    return 0 if receipt['status'] == 'pass' else 1


if __name__ == '__main__':
    sys.exit(main())
