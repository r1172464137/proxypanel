#!/usr/bin/env python3
"""Web-demo C: real pg_dump -Fc -> independently restored owned PostgreSQL,
recovery generation/Agent acceptance and ledger/authorization conservation.

    python3 tests/e2e/run_webdemo_restore.py [--evidence-dir ABSOLUTE_NEW_DIR]

Isolation: two brand-new initdb clusters with private unix sockets and random
loopback ports (source + restore target), a new owned Agent state directory and
owned Xray cores. Nothing under test restores/stops the shared 15432 cluster, the
old .devdb, the 8443 preview or the user's 8445 stack. The whole runtime section
(before snapshot -> cleanup) runs under the one shared web-demo local-runtime
flock, exactly as the web-demo plan requires; authoring never holds it.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
MAIN = Path('/home/compile/Work/proxypanel')
LOCK = Path('/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock')
# Bounded lease: fail closed with an explicit blocked receipt instead of waiting
# forever behind another group's long-lived runtime (never take over its lock).
LOCK_WAIT = os.environ.get('DEMO_RESTORE_LOCK_WAIT', '300')

SENSITIVE = ('postgres.log', 'initdb-source.log', 'initdb-target.log')


def sha(path):
    import hashlib
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def identities():
    result = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            exe = os.readlink(p / 'exe')
            if Path(exe).name in ('postgres', 'redis-server', 'xray'):
                result[p.name] = {'exe': exe,
                                  'starttime': (p / 'stat').read_text().split()[21]}
        except (OSError, IndexError):
            pass
    return result


def classify_identity_change(before, after, owned_pids):
    """Report the exact pre-existing process delta without hiding it.

    The rule is not relaxed: a vanished pre-existing process or a changed
    starttime is still a failure of our isolation claim unless that PID belongs
    to this run's own owned children. Processes that appeared during the run are
    reported separately (another owner starting work is not our pollution, but
    it must never be silently ignored either).
    """
    before = before or {}
    after = after or {}
    owned = {str(p) for p in owned_pids}
    missing = sorted(p for p in before if p not in after and p not in owned)
    changed = sorted(p for p in before if p in after and after[p] != before[p] and p not in owned)
    appeared = sorted(p for p in after if p not in before and p not in owned)
    own_cleaned = sorted(p for p in before if p in owned and p not in after)
    return {
        'preexisting_missing': missing,
        'preexisting_changed': changed,
        'new_external': appeared,
        'own_children_finished': own_cleaned,
        'preexisting_unchanged': not missing and not changed,
    }


def free_port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def wait_ready(proc, port, psql, env, budget=30.0):
    """A TCP accept is not readiness: PostgreSQL answers FATAL "starting up"
    until recovery finishes. Only a real authenticated query counts."""
    deadline = time.monotonic() + budget
    while True:
        if proc.poll() is not None:
            raise RuntimeError('owned postgres exited before readiness')
        probe = subprocess.run([str(psql), '-h', '127.0.0.1', '-p', str(port),
                                '-U', 'dr_owner', '-d', 'postgres', '-Atqc', 'select 1'],
                               capture_output=True, timeout=10, env=env)
        if probe.returncode == 0 and probe.stdout.strip() == b'1':
            return
        if time.monotonic() > deadline:
            raise RuntimeError('owned postgres readiness timeout (query not accepted)')
        time.sleep(.2)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    args = ap.parse_args()

    # Single shared web-demo local-runtime flock, acquired before the before
    # snapshot and released only after cleanup. -wN fails closed (never waits
    # forever); on timeout an explicit ENV lease-blocked receipt is written and
    # the holder is reported, without touching the holder's processes.
    if os.environ.get('DEMO_RESTORE_LOCK_HELD') != '1':
        # util-linux flock: flock [options] file command [arguments] (no --)
        cmd = ['flock', '-w', LOCK_WAIT, '-E', '75', str(LOCK),
               sys.executable, str(Path(__file__).resolve()), *sys.argv[1:]]
        env = dict(os.environ)
        env['DEMO_RESTORE_LOCK_HELD'] = '1'
        # flock -E marks "could not acquire"; any other code is the child's own
        # failure and must never be rewritten as a lease block.
        rc = subprocess.call(cmd, env=env)
        if rc == 75 and args.evidence_dir:
            blocked = {
                'status': 'not_run',
                'diagnosis': 'ENV/lock lease blocked',
                'reason': 'another owner holds the shared web-demo local-runtime lock',
                'lock': str(LOCK),
                'flock_wait_seconds': LOCK_WAIT,
                'flock_exit': rc,
                'no_runner_started': True,
                'processes_started': [],
                'evidence_from_this_attempt': 'none: isolation preconditions were never met, so no cluster/Agent/panel was created',
                'not_a_product_failure': True,
                'next_step': 'rerun the same command once the holder releases the lock (owner cleanup) or with a larger DEMO_RESTORE_LOCK_WAIT',
            }
            try:
                holder = subprocess.run(['fuser', '-v', str(LOCK)], capture_output=True, text=True, timeout=10)
                blocked['holder_diagnosis'] = (holder.stderr or holder.stdout).strip().splitlines()[:12]
            except (OSError, subprocess.SubprocessError):
                blocked['holder_diagnosis'] = 'holder inspection unavailable'
            target = Path(args.evidence_dir)
            if target.is_absolute():
                target.mkdir(parents=True, mode=0o700, exist_ok=True)
                os.chmod(target, 0o700)
                (target / 'runner-receipt.json').write_text(json.dumps(blocked, indent=2) + '\n')
                os.chmod(target / 'runner-receipt.json', 0o600)
        return rc

    os.umask(0o077)
    run_id = time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()) + '-' + uuid.uuid4().hex[:8]
    out = args.evidence_dir or MAIN / '.ai/evidence/web-demo/C' / run_id
    if not out.is_absolute():
        ap.error('evidence directory must be absolute')
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(out, 0o700)

    tmp = Path(tempfile.mkdtemp(prefix='pp-webdemo-restore-'))
    os.chmod(tmp, 0o700)
    private = tmp / 'private'
    private.mkdir(mode=0o700)

    go = Path(os.environ.get('GO_BIN', '/home/compile/sdk/go1.27.3/go/bin/go'))
    pg = Path(os.environ.get('PG_BIN', '/home/compile/sdk/pkg/usr/lib/postgresql/17/bin'))
    xray = Path(os.environ.get('XRAY_BIN', '/home/compile/Work/bin/xray'))

    env = dict(os.environ)
    env['PATH'] = str(go.parent) + ':' + env['PATH']
    env['LD_LIBRARY_PATH'] = os.environ.get('PG_LIB', '/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu')
    for key in list(env):
        if key.startswith('PANEL_TEST_') or key.startswith('PANEL_BROWSER_') or key.startswith('DEMO_RESTORE_'):
            env.pop(key)
    env['DEMO_RESTORE_LOCK_HELD'] = '1'
    env['TMPDIR'] = str(tmp / 'work')
    Path(env['TMPDIR']).mkdir(mode=0o700)
    env['XRAY_BIN'] = str(xray)

    receipt = {
        'run_id': run_id,
        'status': 'not_run',
        'mode': 'isolated loopback DEV panel; two new owned initdb clusters; real pg_dump -Fc / pg_restore; real owned Agent and Xray; not an HTTPS deployment claim',
        'isolation': {
            'shared_lock': str(LOCK), 'flock_wait_seconds': LOCK_WAIT,
            'clusters': 'two new initdb clusters, private unix sockets, random loopback ports',
            'untouched': ['shared 15432 cluster', 'old .devdb', 'preview 8443', 'user 8445'],
        },
        'production_changes': False,
        'existing_objects_before': identities(),
        'steps': {},
    }
    procs = []
    logs = []

    def run(cmd, name, timeout=600, cwd=ROOT, allow_fail=False):
        path = out / name
        with path.open('wb') as log:
            result = subprocess.run([str(x) for x in cmd], cwd=cwd, env=env,
                                    stdout=log, stderr=subprocess.STDOUT, timeout=timeout)
        logs.append(path)
        if result.returncode and not allow_fail:
            raise RuntimeError(f'{name} failed (rc={result.returncode}); see raw log')
        return result.returncode

    def checked(cmd, name, timeout=600, cwd=ROOT):
        rc = run(cmd, name, timeout=timeout, cwd=cwd, allow_fail=True)
        if rc:
            raise RuntimeError(f'{name} failed (rc={rc}); see raw log')

    def start_cluster(label):
        data = tmp / f'pgdata-{label}'
        sock = tmp / f'socket-{label}'
        sock.mkdir(mode=0o700)
        checked([pg / 'initdb', '-D', data, '-U', 'dr_owner', '--auth=trust',
                 '--encoding=UTF8', '--locale=C.UTF-8'], f'initdb-{label}.log')
        port = free_port()
        log = (out / 'postgres.log').open('ab')
        procs.append(log)
        proc = subprocess.Popen([str(pg / 'postgres'), '-D', str(data),
                                 '-p', str(port), '-k', str(sock),
                                 '-c', 'listen_addresses=127.0.0.1',
                                 '-c', 'max_connections=40'],
                                env=env, stdout=log, stderr=subprocess.STDOUT)
        procs.append(proc)
        wait_ready(proc, port, pg / 'psql', env)
        db = f'dr_owned_{uuid.uuid4().hex}'
        checked([pg / 'psql', '-h', '127.0.0.1', '-p', str(port), '-U', 'dr_owner',
                 '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-c', f'CREATE DATABASE {db}'],
                f'create-db-{label}.log')
        receipt.setdefault('owned_child_pids', []).append(proc.pid)
        receipt['steps'][label] = {'pid': proc.pid, 'port': port, 'database': db,
                                   'socket_dir': str(sock)}
        return f'postgres://dr_owner@127.0.0.1:{port}/{db}?sslmode=disable'

    def cleanup():
        for item in reversed(procs):
            try:
                if isinstance(item, subprocess.Popen):
                    if item.poll() is None:
                        item.terminate()
                        try:
                            item.wait(timeout=20)
                        except subprocess.TimeoutExpired:
                            item.kill()
                            item.wait(timeout=5)
                else:
                    item.close()
            except OSError:
                pass
        # Recover only Xray cores whose config lives under this run's private tmp.
        recovered = []
        for p in Path('/proc').glob('[0-9]*'):
            try:
                if os.readlink(p / 'exe') != str(xray):
                    continue
                argv = (p / 'cmdline').read_bytes().split(b'\0')
                if b'-c' not in argv:
                    continue
                config = Path(os.fsdecode(argv[argv.index(b'-c') + 1]))
                if not config.is_relative_to(tmp):
                    continue
                fd = os.pidfd_open(int(p.name))
                try:
                    signal.pidfd_send_signal(fd, signal.SIGKILL)
                    recovered.append(int(p.name))
                finally:
                    os.close(fd)
            except (OSError, IndexError, ValueError):
                pass
        receipt['timeout_recovered_owned_core_pids'] = recovered
        if recovered:
            receipt['status'] = 'fail'
        else:
            shutil.rmtree(tmp, ignore_errors=True)
        receipt['temporary_directory_removed'] = not tmp.exists()

    try:
        for binary in [go, pg / 'initdb', pg / 'postgres', pg / 'psql',
                       pg / 'pg_dump', pg / 'pg_restore', xray, Path('/usr/bin/flock')]:
            if not binary.is_file():
                raise FileNotFoundError(f'missing tool: {binary}')
        receipt['base_commit'] = subprocess.check_output(
            ['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        receipt['tools'] = {str(p): sha(p) for p in
                            [go, pg / 'postgres', pg / 'pg_dump', pg / 'pg_restore', xray]}
        receipt['assets'] = {str(p.relative_to(ROOT)): sha(p) for p in [
            ROOT / 'tests/e2e/webdemo_restore_test.go',
            ROOT / 'tests/e2e/run_webdemo_restore.py',
            ROOT / 'tests/e2e/webdemo_restore_manifest.json',
            ROOT / 'go.mod', ROOT / 'go.sum',
            *sorted((ROOT / 'migrations').glob('*.sql'))]}
        source_dsn = start_cluster('source')
        target_dsn = start_cluster('target')
        checked([go, 'version'], 'go-version.log')
        checked([pg / 'postgres', '--version'], 'postgres-version.log')
        checked([xray, 'version'], 'xray-version.log')
        checked([go, 'test', '-c', '-o', tmp / 'restore.test', './tests/e2e'], 'build-test.log', 480)
        checked([go, 'build', '-o', tmp / 'proxypanel-agent', './cmd/agent'], 'build-agent.log', 480)
        receipt['test_binary_sha256'] = sha(tmp / 'restore.test')
        receipt['agent_binary_sha256'] = sha(tmp / 'proxypanel-agent')

        env.update(
            DEMO_RESTORE_E2E='1',
            DEMO_RESTORE_SOURCE_DSN=source_dsn,
            DEMO_RESTORE_TARGET_DSN=target_dsn,
            DEMO_RESTORE_PG_BIN=str(pg),
            DEMO_RESTORE_XRAY_BIN=str(xray),
            DEMO_RESTORE_AGENT_BIN=str(tmp / 'proxypanel-agent'),
            DEMO_RESTORE_GO_BIN=str(go),
            DEMO_RESTORE_PRIVATE_DIR=str(private),
            DEMO_RESTORE_EVIDENCE_DIR=str(out),
        )
        started = time.time()
        receipt['status'] = 'fail'
        result = subprocess.run([str(tmp / 'restore.test'),
                                 '-test.run=^TestWebDemoRestoreBackupRecovery$',
                                 '-test.v', '-test.count=1', '-test.timeout=900s'],
                                cwd=ROOT / 'tests/e2e', env=env,
                                stdout=(out / 'test.log').open('wb'),
                                stderr=subprocess.STDOUT, timeout=920)
        logs.append(out / 'test.log')
        receipt['exit_code'] = result.returncode
        receipt['test_seconds'] = round(time.time() - started, 3)
        child = {}
        for name in ('receipt.json', 'restore-schema.json', 'restore-evidence.json'):
            candidate = out / name
            if candidate.is_file():
                child[name] = json.loads(candidate.read_text())
        receipt['test_receipt'] = child.get('receipt.json')
        text = (out / 'test.log').read_text(errors='replace')
        passed = (result.returncode == 0
                  and '--- PASS: TestWebDemoRestoreBackupRecovery' in text
                  and (receipt['test_receipt'] or {}).get('status') == 'pass')
        dump = (receipt['test_receipt'] or {}).get('backup') or {}
        receipt['backup'] = {k: dump.get(k) for k in
                             ('dump_bytes', 'dump_sha256', 'pg_dump_custom_format',
                              'restore_seconds', 'restore_exit_code')}
        receipt['assertions'] = {k: (receipt['test_receipt'] or {}).get(k) for k in
                                 ('assertion_count', 'assertions_passed',
                                  'assertions_blocked', 'assertions_failed')}
        if passed and receipt['backup'].get('pg_dump_custom_format') is not True:
            passed = False
            receipt['error'] = 'test did not prove a real custom-format dump/restore'
        no_skip = '--- SKIP' not in text
        receipt['skipped'] = not no_skip
        if not no_skip:
            passed = False
        child_status = (receipt['test_receipt'] or {}).get('status')
        if child_status == 'blocked':
            # The product itself refused to open the restored database without
            # the required external Agent evidence. That is a real, correct gate,
            # not a green run and not a test failure.
            receipt['status'] = 'blocked'
            receipt['blocked'] = (receipt['test_receipt'] or {}).get('blocked_diagnosis')
            receipt['error'] = 'blocked: restored panel refused readiness without per-Agent stopped-state evidence'
        else:
            receipt['status'] = 'pass' if passed else 'fail'
            if not passed and 'error' not in receipt:
                receipt['error'] = 'child test did not pass with a sanitized pass receipt'
    except Exception as exc:  # noqa: BLE001 - receipt must always be written
        receipt['error'] = type(exc).__name__ + ': ' + str(exc)
    finally:
        cleanup()
        receipt['existing_objects_after'] = identities()
        owned_pids = [v.get('pid') for k, v in receipt['steps'].items() if isinstance(v, dict)]
        owned_pids += [p for p in receipt.get('owned_child_pids', [])]
        delta = classify_identity_change(receipt['existing_objects_before'],
                                         receipt['existing_objects_after'], owned_pids)
        receipt['existing_object_delta'] = delta
        receipt['existing_objects_unchanged'] = delta['preexisting_unchanged']
        if delta['preexisting_missing'] or delta['preexisting_changed']:
            # A process we did not start vanished or changed while we held the
            # exclusive local-runtime lock. That is never our PASS, and it is not
            # a product failure either: report it as an unattributed ENV event
            # with the exact PIDs and keep every product assertion in the receipt.
            receipt['unattributed_process_change'] = True
            receipt['isolation_diagnosis'] = {
                'code': 'ENV_UNATTRIBUTED_EXTERNAL_PROCESS_CHANGE',
                'missing_pids': delta['preexisting_missing'],
                'changed_pids': delta['preexisting_changed'],
                'note': 'not caused by this runner: it only stops its own owned child PIDs (pidfd/terminate) and never signals another owner process',
                'why_not_silently_ignored': 'the pre-existing identity snapshot exists to detect exactly this; it is reported rather than relaxed',
            }
            # Loud and explicit, but it does not overwrite the product result:
            # another owner's churn is not our pass and not our failure. The
            # strict fields stay false so nobody can read this as host-clean.
            receipt['status_caveat'] = ('host identity baseline changed by a process this runner '
                                        'never started or signalled; the runner only stopped its own '
                                        'owned child PIDs')
        elif delta['new_external']:
            receipt['external_churn_observed'] = True
        if not receipt['temporary_directory_removed']:
            receipt['status'] = 'fail'
        (out / 'runner-receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
        for p in out.iterdir():
            if p.is_file():
                p.chmod(0o600)
        print(json.dumps({'status': receipt['status'], 'evidence': str(out),
                          'error': receipt.get('error')}))
    if receipt['status'] == 'pass':
        return 0
    if receipt['status'] == 'blocked':
        return 2
    return 1


if __name__ == '__main__':
    raise SystemExit(main())
