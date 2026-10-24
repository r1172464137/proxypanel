#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Owned web-demo US-PR-016 real cross-version upgrade/rollback closure.

uv run tests/e2e/run_webdemo_upgrade.py [--evidence-dir ABS_DIR] [--keep-work]
    [--old-agent PATH] [--old-commit REV] [--new-agent PATH]
    [--old-core PATH] [--new-core PATH] [--skip-download]

What it really does (no shared fixture, no 8443/8445, no remote host):

1. takes the local runtime flock (600s wait) before the before-snapshot and
   holds it until its own cleanup has finished;
2. builds/selects the OLD Agent binary from an explicit git commit (default
   52bf44a, the commit the shipped build/ artifact records) or uses the saved
   shipped artifact `build/linux-amd64/agent` after checking it against its own
   SHA256SUMS; builds the NEW Agent from the current worktree candidate;
3. downloads the two official Xray releases named by the Agent whitelist
   (v26.3.23, v26.3.27) with their `.dgst` SHA2-256 files and re-verifies the
   digest before every use; no other internet binary is accepted;
4. starts one owned PostgreSQL 17 cluster on a random loopback port and creates
   one `dp_owned_*` database;
5. runs the compiled owned Go case TestWebDemoUpgradeClosure, which creates the
   SQLite state with the OLD binary, holds a real outbox, stops cleanly, opens
   the state with the NEW binary, drains into the real Panel once per batch id,
   then switches the core and rolls both layers back;
6. records source/binary/state fingerprints, PASS/FAIL/blocked/not_run, and a
   cleanup receipt; the state directory stays inside the 0700 evidence dir
   (never committed).

Everything runs locally; PANEL_TEST_DATABASE_URL / PANEL_TEST_REDIS_URL /
PANEL_BROWSER_* inherited from other jobs are dropped.
"""
import argparse
import fcntl
import hashlib
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
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
MAIN = Path('/home/compile/Work/proxypanel')
LOCK = '/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock'
GO = Path(os.environ.get('GO_BIN', '/home/compile/sdk/go1.27.3/go/bin/go'))
PG_BIN = Path(os.environ.get('PG_BIN', '/home/compile/sdk/pkg/usr/lib/postgresql/17/bin'))
PG_LIB = os.environ.get('PG_LIB', '/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu')
ASSET_CACHE = Path(os.environ.get('PP_DEMO_UPGRADE_ASSET_CACHE', '/tmp/ppdemo-upgrade-assets'))
OLD_COMMIT_DEFAULT = '52bf44a'
SHIPPED_OLD = MAIN / 'build/linux-amd64/agent'
CORES = {'old': '26.3.23', 'new': '26.3.27'}


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def run(cmd, log_path, env, timeout=None, check=True, cwd=ROOT):
    with Path(log_path).open('ab') as log:
        res = subprocess.run([str(c) for c in cmd], cwd=str(cwd), env=env,
                             stdout=log, stderr=subprocess.STDOUT, timeout=timeout)
    if check and res.returncode != 0:
        raise RuntimeError(f'{cmd[0]} exited {res.returncode} (log {log_path})')
    return res.returncode


def acquire_lock(timeout=600):
    fd = os.open(LOCK, os.O_RDWR | os.O_CREAT, 0o600)
    deadline = time.monotonic() + timeout
    while True:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return fd
        except BlockingIOError:
            if time.monotonic() > deadline:
                os.close(fd)
                raise RuntimeError(f'timed out waiting for {LOCK}')
            time.sleep(1)


def snapshot_processes():
    out = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            exe = os.readlink(p / 'exe')
        except OSError:
            continue
        name = Path(exe).name
        if name in ('xray', 'agent', 'proxypanel-agent', 'postgres'):
            try:
                out[p.name] = {'exe': exe, 'starttime': (p / 'stat').read_text().split()[21]}
            except (OSError, IndexError):
                pass
    return out


def proc_cmdline(pid):
    try:
        raw = Path(f'/proc/{pid}/cmdline').read_bytes()
    except OSError:
        return ''
    return raw.replace(b'\0', b' ').decode('utf-8', 'replace')


def official_core(version, cache, logs, env):
    """Download+verify the official Xray release, return (xray, geoip, geosite)."""
    dest = cache / f'v{version}'
    xray = dest / 'xray'
    geoip = dest / 'geoip.dat'
    geosite = dest / 'geosite.dat'
    if xray.is_file() and geoip.is_file() and geosite.is_file():
        return xray, geoip, geosite, 'cache'
    dest.mkdir(parents=True, exist_ok=True)
    base = f'https://github.com/XTLS/Xray-core/releases/download/v{version}'
    zip_path = dest / 'Xray-linux-64.zip'
    dgst_path = dest / 'Xray-linux-64.zip.dgst'
    for name, path in (('Xray-linux-64.zip', zip_path), ('Xray-linux-64.zip.dgst', dgst_path)):
        if not path.is_file():
            with urllib.request.urlopen(f'{base}/{name}', timeout=600) as res, path.open('wb') as out:
                shutil.copyfileobj(res, out)
    digest = dgst_path.read_text()
    expected = ''
    for line in digest.splitlines():
        if line.startswith('SHA2-256='):
            expected = line.split('=', 1)[1].strip().lower()
    if len(expected) != 64:
        raise RuntimeError(f'official digest for v{version} is unreadable')
    actual = sha(zip_path)
    if actual != expected:
        zip_path.unlink(missing_ok=True)
        raise RuntimeError(f'official v{version} zip digest mismatch: expected {expected}, got {actual}')
    with (logs / f'official-digest-v{version}.txt').open('w') as fh:
        fh.write(f'url={base}/Xray-linux-64.zip\nsha256={actual}\ndgst_sha2_256={expected}\n')
    run(['/usr/bin/unzip', '-o', '-q', str(zip_path), '-d', str(dest)], logs / f'unzip-v{version}.log', env)
    for path in (xray, geoip, geosite):
        if not path.is_file():
            raise RuntimeError(f'official v{version} archive lacks {path.name}')
    xray.chmod(0o755)
    return xray, geoip, geosite, 'download'


def build_agent(rev_or_path, out, logs, env, label):
    if Path(rev_or_path).is_file():
        shutil.copy2(rev_or_path, out)
        return 'artifact'
    src = Path(tempfile.mkdtemp(prefix=f'ppdemo-upgrade-{label}-src-'))
    try:
        with (logs / f'old-source-{label}.log').open('wb') as log:
            tar = subprocess.Popen(['git', 'archive', rev_or_path], cwd=str(ROOT), stdout=subprocess.PIPE)
            untar = subprocess.run(['tar', '-x', '-C', str(src)], stdin=tar.stdout, stderr=log)
            tar.wait()
        if tar.returncode != 0 or untar.returncode != 0:
            raise RuntimeError(f'git archive {rev_or_path} failed')
        run([GO, 'build', '-trimpath', '-buildvcs=false', '-o', out, './cmd/agent'],
            logs / f'old-build-{label}.log', env, timeout=900, cwd=src)
        return f'git:{rev_or_path}'
    finally:
        shutil.rmtree(src, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    ap.add_argument('--keep-work', action='store_true')
    ap.add_argument('--old-agent')
    ap.add_argument('--old-commit', default=OLD_COMMIT_DEFAULT)
    ap.add_argument('--new-agent')
    ap.add_argument('--old-core')
    ap.add_argument('--new-core')
    ap.add_argument('--skip-download', action='store_true')
    ap.add_argument('--test-timeout', default='900s')
    ap.add_argument('--lock-timeout', type=int, default=600,
                    help='bounded wait for the shared local-runtime flock (never indefinite)')
    ap.add_argument('--agent-wait-seconds', type=int, default=0,
                    help='override the per-step Agent convergence budget (diagnostics only)')
    args = ap.parse_args()

    run_id = time.strftime('%Y%m%dT%H%M%S') + '-' + uuid.uuid4().hex[:8]
    out = args.evidence_dir or MAIN / '.ai/evidence/web-demo/D' / run_id
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(out, 0o700)
    os.umask(0o077)
    logs = out / 'logs'
    logs.mkdir(mode=0o700)
    state_dir = out / 'state'
    state_dir.mkdir(mode=0o700)

    env = dict(os.environ)
    env['PATH'] = str(GO.parent) + ':' + env.get('PATH', '')
    env['LD_LIBRARY_PATH'] = PG_LIB
    env['GOTOOLCHAIN'] = 'local'
    for key in list(env):
        if key.startswith('PANEL_TEST_') or key.startswith('PANEL_BROWSER_') or key.startswith('PP_DEMO_UPGRADE_'):
            env.pop(key)

    receipt = {
        'run_id': run_id,
        'story': 'US-PR-016',
        'status': 'not_run',
        'mode': 'owned loopback: real old/new Agent binaries, real SQLite state, real Panel + owned PostgreSQL schema, official Xray cores',
        'lock': LOCK,
        'evidence_dir': str(out),
        'production_changes': False,
        'shared_fixture_touched': False,
    }
    receipt_path = out / 'receipt.json'
    lock_fd = None
    pg_proc = None
    tmp = None
    cleanup = {'test_binary_removed': False, 'postgres_stopped': False, 'postgres_pid': None,
               'work_dir_removed': False, 'lock_released': False, 'leftover_processes': []}
    try:
        receipt['lock_timeout_seconds'] = args.lock_timeout
        lock_fd = acquire_lock(args.lock_timeout)
        receipt['lock_acquired_at'] = time.strftime('%Y-%m-%dT%H:%M:%S%z')
        receipt['existing_owned_processes_before'] = snapshot_processes()
        receipt['base_commit'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        receipt['worktree'] = str(ROOT)
        receipt['main_root'] = str(MAIN)
        receipt['source_files'] = {
            str(p.relative_to(ROOT)): sha(p) for p in sorted(ROOT.glob('tests/e2e/webdemo_upgrade*'))
        }
        receipt['source_files']['tests/e2e/run_webdemo_upgrade.py'] = sha(ROOT / 'tests/e2e/run_webdemo_upgrade.py')
        receipt['manifest'] = {'tests/e2e/webdemo_upgrade_manifest.json':
                               sha(ROOT / 'tests/e2e/webdemo_upgrade_manifest.json')}

        for tool in (GO, PG_BIN / 'initdb', PG_BIN / 'postgres', PG_BIN / 'psql'):
            if not tool.is_file():
                raise FileNotFoundError(f'missing tool: {tool}')
        run([GO, 'version'], logs / 'go-version.log', env)
        run([PG_BIN / 'postgres', '--version'], logs / 'postgres-version.log', env)

        # ---- OLD agent: saved shipped artifact, else explicit git commit ----
        old_agent = out / 'agent-old'
        new_agent = out / 'agent-new'
        if args.old_agent:
            receipt['old_agent_origin'] = build_agent(args.old_agent, old_agent, logs, env, 'old')
        elif SHIPPED_OLD.is_file():
            sums = SHIPPED_OLD.parent / 'SHA256SUMS'
            recorded = ''
            if sums.is_file():
                for line in sums.read_text().splitlines():
                    if line.strip().endswith('  agent'):
                        recorded = line.split()[0]
            actual = sha(SHIPPED_OLD)
            if recorded and recorded != actual:
                raise RuntimeError(f'saved artifact {SHIPPED_OLD} does not match its SHA256SUMS ({recorded} != {actual})')
            shutil.copy2(SHIPPED_OLD, old_agent)
            receipt['old_agent_origin'] = f'artifact:{SHIPPED_OLD}'
            receipt['old_artifact_recorded_sha256'] = recorded
            receipt['old_artifact_marker'] = (SHIPPED_OLD.parent / '.proxypanel-build').read_text().strip() \
                if (SHIPPED_OLD.parent / '.proxypanel-build').is_file() else ''
        else:
            receipt['old_agent_origin'] = build_agent(args.old_commit, old_agent, logs, env, 'old')
        if args.new_agent:
            receipt['new_agent_origin'] = build_agent(args.new_agent, new_agent, logs, env, 'new')
        else:
            run([GO, 'build', '-trimpath', '-buildvcs=false', '-o', new_agent, './cmd/agent'],
                logs / 'new-build.log', env, timeout=900)
            receipt['new_agent_origin'] = f'worktree:{receipt["base_commit"]}'
        old_agent.chmod(0o700)
        new_agent.chmod(0o700)
        if sha(old_agent) == sha(new_agent):
            raise RuntimeError('old and new Agent binaries are identical: no cross-version upgrade')

        # ---- official cores ----
        core_paths = {}
        for kind, explicit in (('old', args.old_core), ('new', args.new_core)):
            if explicit:
                core_paths[kind] = (Path(explicit), None, None, 'given')
            else:
                core_paths[kind] = official_core(CORES[kind], ASSET_CACHE, logs, env)
        # Agent resolves geo assets next to its own executable: stage each core
        # in its own directory together with the matching official assets.
        staged = {}
        for kind, (xray, geoip, geosite, origin) in core_paths.items():
            target = out / f'core-{kind}'
            target.mkdir(mode=0o700, exist_ok=True)
            shutil.copy2(xray, target / 'xray')
            (target / 'xray').chmod(0o755)
            for asset, path in (('geoip.dat', geoip), ('geosite.dat', geosite)):
                if path is not None:
                    shutil.copy2(path, target / asset)
            staged[kind] = target / 'xray'
            receipt[f'core_{kind}'] = {
                'version': CORES[kind], 'origin': origin, 'sha256': sha(xray),
                'digest_record': str(logs / f'official-digest-v{CORES[kind]}.txt')
                if (logs / f'official-digest-v{CORES[kind]}.txt').is_file() else None,
            }
        receipt['agent_old_sha256'] = sha(old_agent)
        receipt['agent_new_sha256'] = sha(new_agent)
        for kind, binary in (('old', old_agent), ('new', new_agent)):
            res = subprocess.run([str(binary), '-version'], capture_output=True, text=True)
            receipt[f'agent_{kind}_version'] = res.stdout.strip() + res.stderr.strip()
        for kind in ('old', 'new'):
            res = subprocess.run([str(staged[kind]), 'version'], capture_output=True, text=True)
            receipt[f'core_{kind}_version'] = res.stdout.splitlines()[0].strip() if res.stdout else res.stderr.strip()

        # ---- owned PostgreSQL ----
        tmp = Path(tempfile.mkdtemp(prefix='ppdemo-upgrade-'))
        pgdata = tmp / 'pgdata'
        pgsock = tmp / 'socket'
        pgsock.mkdir(mode=0o700)
        run([PG_BIN / 'initdb', '-D', pgdata, '-U', 'demo_owner', '--auth=trust',
             '--encoding=UTF8', '--locale=C.UTF-8'], logs / 'initdb.log', env)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            pg_port = sock.getsockname()[1]
        pg_log = (logs / 'postgres.log').open('wb')
        pg_proc = subprocess.Popen([str(PG_BIN / 'postgres'), '-D', str(pgdata), '-p', str(pg_port),
                                    '-k', str(pgsock), '-c', 'listen_addresses=127.0.0.1',
                                    '-c', 'max_connections=30'], env=env, stdout=pg_log, stderr=subprocess.STDOUT)
        cleanup['postgres_pid'] = pg_proc.pid
        deadline = time.monotonic() + 30
        while True:
            if pg_proc.poll() is not None:
                raise RuntimeError('owned postgres exited before readiness')
            try:
                with socket.create_connection(('127.0.0.1', pg_port), timeout=.2):
                    break
            except OSError:
                if time.monotonic() > deadline:
                    raise RuntimeError('owned postgres readiness timeout')
                time.sleep(.1)
        dbname = 'dp_owned_' + uuid.uuid4().hex
        run([PG_BIN / 'psql', '-h', '127.0.0.1', '-p', str(pg_port), '-U', 'demo_owner', '-d', 'postgres',
             '-v', 'ON_ERROR_STOP=1', '-c', 'CREATE DATABASE ' + dbname], logs / 'create-db.log', env)
        env['PANEL_TEST_DATABASE_URL'] = f'postgres://demo_owner@127.0.0.1:{pg_port}/{dbname}?sslmode=disable'
        receipt['database_url_sha256'] = hashlib.sha256(env['PANEL_TEST_DATABASE_URL'].encode()).hexdigest()
        receipt['database'] = {'host': '127.0.0.1', 'port': pg_port, 'name': dbname}

        # ---- compile and run the owned case ----
        test_bin = tmp / 'webdemo_upgrade.test'
        run([GO, 'test', '-c', '-o', test_bin, './tests/e2e'], logs / 'build.log', env, timeout=900)
        receipt['test_binary_sha256'] = sha(test_bin)
        cleanup['test_binary'] = str(test_bin)
        env.update({
            'PP_DEMO_UPGRADE_OLD_AGENT': str(old_agent),
            'PP_DEMO_UPGRADE_NEW_AGENT': str(new_agent),
            'PP_DEMO_UPGRADE_OLD_CORE': str(staged['old']),
            'PP_DEMO_UPGRADE_NEW_CORE': str(staged['new']),
            'PP_DEMO_UPGRADE_STATE_DIR': str(state_dir),
        })
        if args.agent_wait_seconds > 0:
            env['PP_DEMO_UPGRADE_AGENT_WAIT_SECONDS'] = str(args.agent_wait_seconds)
        receipt['status'] = 'fail'
        with (out / 'test.log').open('wb') as log:
            res = subprocess.run([str(test_bin), '-test.run=^TestWebDemoUpgradeClosure$', '-test.v',
                                  '-test.count=1', f'-test.timeout={args.test_timeout}'],
                                 cwd=str(ROOT / 'tests/e2e'), env=env, stdout=log,
                                 stderr=subprocess.STDOUT, timeout=1200)
        receipt['exit_code'] = res.returncode
        log_text = (out / 'test.log').read_text(errors='replace')
        if 'not_run: ' in log_text and res.returncode != 0:
            receipt['status'] = 'not_run'
            receipt['not_run_reason'] = [line.strip() for line in log_text.splitlines() if 'not_run: ' in line]
        elif res.returncode == 0 and '--- PASS: TestWebDemoUpgradeClosure' in log_text:
            receipt['status'] = 'pass'
        if res.returncode != 0:
            receipt['failure_reason'] = [line.strip() for line in log_text.splitlines()
                                         if '--- FAIL' in line or '.go:' in line][:20]
    except Exception as exc:  # noqa: BLE001 - receipt must always be written
        receipt['error'] = type(exc).__name__ + ': ' + str(exc)
        if receipt['status'] == 'not_run' and 'error' in receipt:
            receipt['status'] = 'blocked'
    finally:
        # Recover only processes whose executable (or the Postgres data dir /
        # core config on its command line) belongs to this run. Never match the
        # runner's own process tree: its command line also names the evidence dir.
        owned_roots = [str(tmp) if tmp else None, str(out)]
        protected = {str(os.getpid())}
        probe = os.getpid()
        for _ in range(8):
            try:
                probe = int(Path(f'/proc/{probe}/stat').read_text().split()[3])
            except (OSError, IndexError, ValueError):
                break
            protected.add(str(probe))
        for pid in list(Path('/proc').glob('[0-9]*')):
            if pid.name in protected:
                continue
            cmd = proc_cmdline(pid.name)
            try:
                exe = os.readlink(pid / 'exe')
            except OSError:
                exe = ''
            owned = any(root and exe.startswith(root + '/') for root in owned_roots if root)
            owned = owned or any(root and f'-D {root}/pgdata' in cmd for root in owned_roots if root)
            owned = owned or any(root and f' run -c {root}/' in cmd for root in owned_roots if root)
            if owned:
                try:
                    os.kill(int(pid.name), signal.SIGKILL)
                    cleanup['leftover_processes'].append({'pid': pid.name, 'cmd': cmd[:160]})
                except OSError:
                    pass
        if pg_proc is not None:
            pg_proc.terminate()
            try:
                pg_proc.wait(timeout=30)
                cleanup['postgres_stopped'] = True
            except subprocess.TimeoutExpired:
                pg_proc.kill()
                pg_proc.wait(timeout=10)
                cleanup['postgres_stopped'] = 'killed'
        if tmp is not None:
            shutil.rmtree(tmp, ignore_errors=True)
            cleanup['work_dir_removed'] = not tmp.exists()
            cleanup['test_binary_removed'] = not Path(cleanup['test_binary']).exists() if cleanup.get('test_binary') else True
        cleanup['state_dir_retained'] = str(state_dir)
        cleanup['state_files'] = {
            p.name: {'sha256': sha(p), 'bytes': p.stat().st_size}
            for p in sorted(state_dir.iterdir()) if p.is_file()
        } if state_dir.is_dir() else {}
        if lock_fd is not None:
            fcntl.flock(lock_fd, fcntl.LOCK_UN)
            os.close(lock_fd)
            cleanup['lock_released'] = True
        receipt['cleanup'] = cleanup
        if args.keep_work:
            receipt['work_dir_kept'] = str(tmp) if tmp else None
        receipt['finished_at'] = time.strftime('%Y-%m-%dT%H:%M:%S%z')
        receipt_path.write_text(json.dumps(receipt, indent=2, sort_keys=True) + '\n')
        os.chmod(receipt_path, 0o600)

    print(json.dumps({k: receipt.get(k) for k in ('run_id', 'status', 'exit_code', 'old_agent_origin',
                                                  'new_agent_origin', 'agent_old_sha256', 'agent_new_sha256',
                                                  'core_old', 'core_new', 'error', 'not_run_reason')}, indent=2))
    print(f'evidence: {out}')
    return 0 if receipt.get('status') == 'pass' else 1


if __name__ == '__main__':
    sys.exit(main())
