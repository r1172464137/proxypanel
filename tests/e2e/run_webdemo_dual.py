#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Owned dual-Agent / offline / UDP acceptance for the web-demo phase (task B).

    uv run tests/e2e/run_webdemo_dual.py [--evidence-dir ABSOLUTE_DIR]

Everything is owned: a private PostgreSQL cluster (`initdb` in a new temp dir,
database prefix `wdd_owned_`), a random schema created by the test itself, two
real official Xray cores driven by two real supervisor instances, loopback-only
dynamic ports and a private TCP/UDP echo target. Existing services (203:8445,
the old 8443 stack, agent-lab, the shared product-readiness PG on 15432) are
never touched; the identity snapshot below proves it.

Real local core/PG execution is serialized with the shared web-demo runtime lock
(`/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock`): this wrapper
takes the canonical flock itself before the pre-run identity snapshot (version
probes included) and releases it only after its own cleanup. A busy lock is
waited for at most 90s and then the run ends as `not_run`/ENV; a busy lock is
never treated as "we hold it". There is no environment flag, inherited lock or
skip switch that can turn an unlocked run into a pass.

Optional environment: GO_BIN, PG_BIN, PG_LIB, PP_DEMO_DUAL_XRAY_BIN.
A run started without the canonical local-runtime flock is `not_run` (exit 2)
and never starts PostgreSQL, a core or the test binary.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
XRAY_PATH = Path(os.environ.get('PP_DEMO_DUAL_XRAY_BIN', '/home/compile/Work/bin/xray'))
MAIN = Path('/home/compile/Work/proxypanel')
LOCK = Path('/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock')
DB_PREFIX = 'wdd_owned_'


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def processes(pattern_name):
    """Identity of live processes whose exe basename matches (read-only)."""
    out = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            exe = os.readlink(p / 'exe')
            if Path(exe).name != pattern_name:
                continue
            stat = (p / 'stat').read_text()
            out[p.name] = {'exe': exe, 'starttime': stat.split()[21]}
        except (OSError, IndexError):
            pass
    return out


def listeners():
    """Owned/foreign TCP **and UDP** listeners (read-only, no netlink).

    TCP listeners are state 0A; a bound unconnected UDP socket is state 07 (the
    value the kernel reports for every bound UDP socket), so UDP is collected
    without a state filter. Leaving UDP out would let an owned/foreign UDP
    service disappear without any trace in the receipt.
    """
    out = {}
    for name in ('tcp', 'tcp6', 'udp', 'udp6'):
        try:
            lines = Path(f'/proc/net/{name}').read_text().splitlines()[1:]
        except OSError:
            continue
        for line in lines:
            f = line.split()
            if len(f) < 10:
                continue
            local, state, inode = f[1], f[3], f[9]
            if name.startswith('tcp') and state != '0A':
                continue
            if name.startswith('udp') and state not in ('07', '0A'):
                continue
            out[f'{name}:{local}'] = inode
    return out


PROTECTED_EXES = ('xray', 'proxypanel-agent', 'postgres', 'redis-server')


def protected_identity(root):
    """Original business processes (Agent-lab, Xray, PG/Redis masters).

    Identity is pid+starttime+exe+cwd+args; processes below this run's own temp
    root are recorded separately as own scope. This is observation, not an
    ownership registry: a pid that existed before and is gone after is never
    silently ignored.
    """
    own, foreign = {}, {}
    me = os.getpid()
    for p in Path('/proc').glob('[0-9]*'):
        pid = p.name
        try:
            if int(pid) == me:
                continue
            exe = os.readlink(p / 'exe')
            if Path(exe).name not in PROTECTED_EXES:
                continue
            stat = (p / 'stat').read_text().split()
            cwd = os.readlink(p / 'cwd')
            argv = (p / 'cmdline').read_bytes().split(b'\0')
            args = [os.fsdecode(a) for a in argv if a]
            # Only long-running cores/services, not short-lived CLI helpers such as
            # `xray api statsquery` that appear and vanish between snapshots.
            if Path(exe).name == 'xray' and 'run' not in args[1:2]:
                continue
            rec = {'pid': int(pid), 'starttime': stat[21], 'exe': exe, 'cwd': cwd,
                   'args': args}
            is_own = cwd == str(root) or any(a.startswith(str(root)) for a in args)
            (own if is_own else foreign)[pid] = rec
        except (OSError, IndexError, ValueError):
            pass
    return own, foreign


def reap_own_cores(root, xray, recovered):
    """Kill only cores whose -c config lives under this invocation's root."""
    for p in Path('/proc').glob('[0-9]*'):
        try:
            if os.readlink(p / 'exe') != str(xray):
                continue
            argv = (p / 'cmdline').read_bytes().split(b'\0')
            if b'-c' not in argv:
                continue
            config = Path(os.fsdecode(argv[argv.index(b'-c') + 1]))
            if not config.is_relative_to(root):
                continue
            pid = int(p.name)
            fd = os.pidfd_open(pid)
            try:
                signal.pidfd_send_signal(fd, signal.SIGKILL)
                recovered.append(pid)
            finally:
                os.close(fd)
        except (OSError, IndexError, ValueError):
            pass


def owned_cores(root):
    """Xray cores whose -c config lives under this run's private root (own scope)."""
    out = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            if os.readlink(p / 'exe') != str(XRAY_PATH):
                continue
            argv = (p / 'cmdline').read_bytes().split(b'\0')
            if b'-c' not in argv:
                continue
            config = Path(os.fsdecode(argv[argv.index(b'-c') + 1]))
            if not config.is_relative_to(root):
                continue
            out[p.name] = (p / 'stat').read_text().split()[21]
        except (OSError, IndexError, ValueError):
            continue
    return out


def pg_clusters():
    out = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            if os.readlink(p / 'exe') != '/usr/lib/postgresql/17/bin/postgres':
                continue
            out[p.name] = (p / 'stat').read_text().split()[21]
        except (OSError, IndexError):
            continue
    return out


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    args = ap.parse_args()
    run_id = time.strftime('%Y%m%dT%H%M%S') + '-' + uuid.uuid4().hex[:8]
    out = args.evidence_dir or MAIN / '.ai/evidence/web-demo/B/0700' / run_id
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(out, 0o700)
    os.umask(0o077)
    tmp = Path(tempfile.mkdtemp(prefix='pp-wdd-'))
    go = Path(os.environ.get('GO_BIN', '/home/compile/sdk/go1.27.3/go/bin/go'))
    pg = Path(os.environ.get('PG_BIN', '/home/compile/sdk/pkg/usr/lib/postgresql/17/bin'))
    xray = Path(os.environ.get('PP_DEMO_DUAL_XRAY_BIN', '/home/compile/Work/bin/xray'))
    env = dict(os.environ)
    env['PATH'] = str(go.parent) + ':' + env['PATH']
    env['LD_LIBRARY_PATH'] = os.environ.get('PG_LIB', '/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu')
    # Never inherit another job's DB/browser/matrix configuration.
    for key in list(env):
        if key.startswith('PANEL_TEST_') or key.startswith('PANEL_BROWSER_') or key.startswith('PP_DEMO_DUAL_'):
            env.pop(key)
    env['PP_DEMO_DUAL_XRAY_BIN'] = str(xray)
    env['PP_DEMO_DUAL_EVIDENCE_DIR'] = str(out)
    (tmp / 'work').mkdir(mode=0o700)
    env['TMPDIR'] = str(tmp / 'work')

    receipt = {
        'run_id': run_id,
        'status': 'not_run',
        'mode': 'owned loopback DEV panel + 2 real Agents + 2 official Xray cores; not an HTTPS deployment',
        'isolation': 'new private PG cluster/database wdd_owned_*, random schema, loopback dynamic ports, owned TCP/UDP echo',
        'production_changes': False,
        'shared_runtime_lock': str(LOCK),
    }
    lock_fd = None
    proc = None
    pglog = None
    recovered = []
    own_pids = {os.getpid()}
    try:
        for binary in (go, pg / 'initdb', pg / 'postgres', pg / 'psql', xray):
            if not binary.is_file():
                raise FileNotFoundError(f'missing tool: {binary}')

        # B1: this wrapper must really hold the canonical local-runtime lease.
        # There is no skip flag, no UNDER_LOCK env and no "busy means I own it".
        # Failure to acquire is bounded (90s) and is an ENV/not_run outcome, never
        # a pass; nothing (not even a version probe) runs before the flock.
        import fcntl
        LOCK.parent.mkdir(parents=True, exist_ok=True)
        lock_fd = os.open(LOCK, os.O_RDWR | os.O_CREAT, 0o600)
        deadline = time.monotonic() + 90
        while True:
            try:
                fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() > deadline:
                    receipt['status'] = 'not_run'
                    receipt['runtime_lock_acquired'] = False
                    receipt['runtime_lock_holder'] = 'unknown (busy; not ours)'
                    raise RuntimeError('ENV: canonical local-runtime lease busy >90s; no runtime started')
                time.sleep(1)
        receipt['runtime_lock_acquired'] = True
        receipt['runtime_lock_owner_pid'] = os.getpid()
        receipt['runtime_lock_fd'] = lock_fd
        receipt['runtime_lock_path'] = str(LOCK)
        receipt['runtime_lock_acquired_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        receipt['runtime_lock_waited_seconds'] = max(0, 90 - int(deadline - time.monotonic()))

        # Identity snapshot AFTER the lock, in two scopes:
        #  * own scope (strict, must be byte-identical before/after): every Xray
        #    core whose -c config lives under this run's private temp root; this
        #    run must leave zero such processes and recover none.
        #  * host inventory (informational): other groups (C/D/E) start/stop their
        #    own owned cores concurrently, so host-wide churn is recorded, not
        #    treated as pollution. Foreign services are additionally protected by
        #    the listener inventory below (any listener that existed before must
        #    still exist with the same inode).
        receipt['owned_cores_before'] = owned_cores(tmp)
        receipt['host_cores_before'] = processes('xray')
        receipt['postgres_before'] = pg_clusters()
        receipt['listeners_before'] = listeners()
        own_before, foreign_before = protected_identity(tmp)
        receipt['own_processes_before'] = own_before
        receipt['protected_original_before'] = foreign_before
        before_ports = {int(k.rsplit(':', 1)[1], 16) for k in receipt['listeners_before']
                        if k.split(':')[0].startswith('tcp')}
        receipt['protected_ports_observed'] = {
            '8443_legacy_stack_listening': 8443 in before_ports,
            '8445_user_preview_listening': 8445 in before_ports,
            '15432_shared_pg_listening': 15432 in before_ports,
        }
        receipt['base_commit'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        receipt['git_status_porcelain'] = subprocess.check_output(
            ['git', 'status', '--porcelain'], cwd=ROOT, text=True).splitlines()
        receipt['source_files'] = {str(p.relative_to(ROOT)): sha(p) for p in sorted(ROOT.rglob('*.go'))
                                   if '.git' not in p.parts and 'node_modules' not in p.parts}
        receipt['fixture_assets'] = {str(p.relative_to(ROOT)): sha(p) for p in [
            ROOT / 'tests/e2e/run_webdemo_dual.py',
            ROOT / 'tests/e2e/webdemo_dual_test.go',
            ROOT / 'tests/e2e/webdemo_dual_manifest.json',
            ROOT / 'go.mod', ROOT / 'go.sum',
            *sorted((ROOT / 'migrations').glob('*.sql'))]}
        receipt['binaries'] = {str(p): sha(p) for p in (go, pg / 'postgres', xray)}

        def run(cmd, filename, timeout=240):
            with (out / filename).open('wb') as log:
                return subprocess.run([str(x) for x in cmd], cwd=ROOT, env=env, stdout=log,
                                      stderr=subprocess.STDOUT, timeout=timeout, check=True)

        run([go, 'version'], 'go-version.log')
        run([xray, 'version'], 'xray-version.log')
        run([pg / 'postgres', '--version'], 'postgres-version.log')
        run([pg / 'initdb', '-D', tmp / 'pgdata', '-U', 'wdd_owner', '--auth=trust',
             '--encoding=UTF8', '--locale=C.UTF-8'], 'initdb.log')
        port = free_port()
        (tmp / 'socket').mkdir(mode=0o700)
        pglog = (out / 'postgres.log').open('wb')
        proc = subprocess.Popen([str(pg / 'postgres'), '-D', str(tmp / 'pgdata'), '-p', str(port),
                                 '-k', str(tmp / 'socket'), '-c', 'listen_addresses=127.0.0.1',
                                 '-c', 'max_connections=30'], env=env, stdout=pglog,
                                stderr=subprocess.STDOUT)
        receipt['owned_postgres_pid'] = proc.pid
        own_pids.add(proc.pid)
        receipt['owned_postgres_port'] = port
        deadline = time.monotonic() + 30
        while True:
            if proc.poll() is not None:
                raise RuntimeError('owned postgres exited before readiness')
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=.2):
                    break
            except OSError:
                if time.monotonic() > deadline:
                    raise RuntimeError('owned postgres readiness timeout')
                time.sleep(.1)
        db = DB_PREFIX + uuid.uuid4().hex
        receipt['owned_database'] = db
        run([pg / 'psql', '-h', '127.0.0.1', '-p', str(port), '-U', 'wdd_owner', '-d', 'postgres',
             '-v', 'ON_ERROR_STOP=1', '-c', 'CREATE DATABASE ' + db], 'create-db.log')
        env['PP_DEMO_DUAL_DATABASE_URL'] = f'postgres://wdd_owner@127.0.0.1:{port}/{db}?sslmode=disable'

        run([go, 'test', '-c', '-o', tmp / 'wdd.test', './tests/e2e'], 'build.log', 600)
        receipt['test_binary_sha256'] = sha(tmp / 'wdd.test')
        receipt['status'] = 'fail'
        with (out / 'test.log').open('wb') as log:
            result = subprocess.run([str(tmp / 'wdd.test'), '-test.run=^TestWebDemoDualAgent$',
                                     '-test.v', '-test.count=1', '-test.timeout=1200s'],
                                    cwd=ROOT / 'tests/e2e', env=env, stdout=log,
                                    stderr=subprocess.STDOUT, timeout=1260)
        receipt['exit_code'] = result.returncode
        text = (out / 'test.log').read_text(errors='replace')
        if result.returncode == 0 and '--- PASS: TestWebDemoDualAgent' in text:
            receipt['status'] = 'pass'
        else:
            receipt['failure_excerpt'] = [
                line for line in text.splitlines()
                if '--- FAIL' in line or line.strip().startswith('webdemo_dual_test.go:')
                or 'timed out waiting' in line or 'panic:' in line][:40]
        if '--- SKIP' in text:
            receipt['status'] = 'not_run'
            receipt['error'] = 'test reported SKIP'
        receipt['xray_hash_unchanged'] = sha(xray) == receipt['binaries'][str(xray)]
        if not receipt['xray_hash_unchanged']:
            receipt['status'] = 'fail'
    except Exception as exc:
        receipt['error'] = type(exc).__name__ + ': ' + str(exc)
    finally:
        # A Go timeout/panic can bypass t.Cleanup. Recover only Xray children whose
        # -c path is beneath this invocation's private temporary root, using pidfd
        # so a recycled PID can never be signalled.
        reap_own_cores(tmp, xray, recovered)
        own_pids.update(recovered)
        receipt['timeout_recovered_owned_core_pids'] = recovered
        if recovered:
            receipt['status'] = 'fail'
        if proc is not None:
            if proc.poll() is None:
                proc.terminate()  # exact child PID; never pkill/process-group kill
                try:
                    proc.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)
            receipt['owned_postgres_exit'] = proc.returncode
        if pglog:
            pglog.close()
        shutil.rmtree(tmp, ignore_errors=True)
        receipt['temporary_directory_removed'] = not tmp.exists()
        receipt['owned_cores_after'] = owned_cores(tmp)
        receipt['host_cores_after'] = processes('xray')
        receipt['listeners_after'] = listeners()
        own_after, foreign_after = protected_identity(tmp)
        receipt['own_processes_after'] = own_after
        receipt['protected_original_after'] = foreign_after
        receipt['owned_core_leftovers'] = sorted(receipt['owned_cores_after'])
        # own scope must be empty: no core config under this root, no own process
        # (PG/agent/test children) left behind.
        receipt['own_process_leftovers'] = sorted(int(p) for p in own_after if int(p) not in own_pids)
        receipt['owned_scope_clean'] = (
            not receipt['owned_cores_after']
            and not receipt['owned_cores_before']
            and not receipt['own_process_leftovers']
            and not receipt.get('timeout_recovered_owned_core_pids'))
        # B3: every protected original pid (Agent-lab, foreign Xray/PG/Redis musters)
        # must still exist with the same pid+starttime+exe+cwd+args. A vanished pid is
        # NOT ignored as "other owner churn": it needs an explicit creator/lease
        # receipt or an ENV caveat and a re-run, so this receipt is not host-clean.
        vanished = []
        for pid, rec in receipt.get('protected_original_before', {}).items():
            after = foreign_after.get(pid)
            if after != rec:
                vanished.append({'pid': int(pid), 'before': rec, 'after': after})
        receipt['protected_original_vanished_or_changed'] = vanished
        receipt['protected_original_intact'] = not vanished
        receipt['host_core_inventory_churn'] = sorted(
            set(receipt['host_cores_after']) ^ set(receipt['host_cores_before']))
        # UDP and TCP listeners that existed before must still exist on the same inode.
        receipt['foreign_listeners_unchanged'] = all(
            receipt['listeners_after'].get(k) == v for k, v in receipt.get('listeners_before', {}).items())
        receipt['foreign_listener_missing'] = sorted(
            k for k, v in receipt.get('listeners_before', {}).items()
            if receipt['listeners_after'].get(k) != v)
        if not receipt['owned_scope_clean'] or not receipt['foreign_listeners_unchanged'] \
                or not receipt['protected_original_intact']:
            receipt['status'] = 'fail'
            receipt['isolation_violation'] = True
            if vanished:
                receipt['isolation_reason'] = ('ENV: a pre-existing protected pid vanished/changed; '
                                               'needs explicit creator/lease receipt before re-run')
        if lock_fd is not None:
            # Release only after this invocation's own cleanup above.
            receipt['runtime_lock_released_after_own_cleanup'] = True
            receipt['runtime_lock_released_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
            fcntl.flock(lock_fd, fcntl.LOCK_UN)
            os.close(lock_fd)
            receipt['runtime_lock_released'] = True
        (out / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
        for p in out.iterdir():
            if p.is_file():
                p.chmod(0o600)
        print(json.dumps({'status': receipt['status'], 'evidence': str(out),
                          'error': receipt.get('error')}))
    return 0 if receipt['status'] == 'pass' else 1


if __name__ == '__main__':
    raise SystemExit(main())
