#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Owned loopback real-core acceptance; no existing services are reconfigured.
uv run tests/e2e/run_data_plane.py [--evidence-dir ABSOLUTE_DIRECTORY]
Optional GO_BIN, PG_BIN, PG_LIB, XRAY_BIN select installed tools.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
MAIN = Path('/home/compile/Work/proxypanel')

def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def existing_cores():
    result = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            exe = os.readlink(p / 'exe')
            if Path(exe).name == 'xray':
                result[p.name] = {'exe': exe, 'starttime': (p / 'stat').read_text().split()[21]}
        except (OSError, IndexError):
            pass
    return result

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    args = ap.parse_args()
    run_id = time.strftime('%Y%m%dT%H%M%S') + '-' + uuid.uuid4().hex[:8]
    out = args.evidence_dir or MAIN / '.ai/evidence/data-plane/product-readiness' / run_id
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(out, 0o700)
    os.umask(0o077)
    tmp = Path(tempfile.mkdtemp(prefix='pp-data-plane-'))
    go = Path(os.environ.get('GO_BIN', '/home/compile/sdk/go1.27.3/go/bin/go'))
    pg = Path(os.environ.get('PG_BIN', '/home/compile/sdk/pkg/usr/lib/postgresql/17/bin'))
    xray = Path(os.environ.get('XRAY_BIN', '/home/compile/Work/bin/xray'))
    env = dict(os.environ)
    env['PATH'] = str(go.parent) + ':' + env['PATH']
    env['LD_LIBRARY_PATH'] = os.environ.get('PG_LIB', '/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu')
    # Never inherit DB/browser/matrix configuration from other acceptance jobs.
    for key in list(env):
        if key.startswith('PANEL_TEST_') or key.startswith('PANEL_BROWSER_'):
            env.pop(key)
    env['XRAY_BIN'] = str(xray)
    (tmp/'work').mkdir(mode=0o700)
    env['TMPDIR'] = str(tmp/'work')
    receipt = {'run_id': run_id, 'status': 'not_run', 'mode': 'loopback DEV Panel; real Agent supervisor; verified ephemeral TLS control hop; not HTTPS deployment', 'existing_cores_before': existing_cores(), 'production_changes': False}
    proc = None
    pglog = None
    def run(cmd, filename, timeout=180):
        with (out / filename).open('wb') as log:
            return subprocess.run([str(x) for x in cmd], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=timeout, check=True)
    try:
        for binary in [go, pg/'initdb', pg/'postgres', pg/'psql', xray]:
            if not binary.is_file():
                raise FileNotFoundError(f'missing tool: {binary}')
        receipt['base_commit'] = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
        receipt['source_files'] = {str(p.relative_to(ROOT)): sha(p) for p in sorted(ROOT.rglob('*.go')) if '.git' not in p.parts}
        receipt['fixture_assets'] = {str(p.relative_to(ROOT)): sha(p) for p in [ROOT/'tests/e2e/run_data_plane.py', ROOT/'tests/e2e/data_plane_manifest.json', ROOT/'go.mod', ROOT/'go.sum', *sorted((ROOT/'migrations').glob('*.sql'))]}
        receipt['binaries'] = {str(p): sha(p) for p in [go, pg/'postgres', xray]}
        run([go, 'version'], 'go-version.log')
        run([xray, 'version'], 'xray-version.log')
        run([pg/'postgres', '--version'], 'postgres-version.log')
        run([pg/'initdb', '-D', tmp/'pgdata', '-U', 'dp_owner', '--auth=trust', '--encoding=UTF8', '--locale=C.UTF-8'], 'initdb.log')
        # Dynamic loopback ports only. The release/bind race is fail-closed.
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        (tmp/'socket').mkdir(mode=0o700)
        pglog = (out/'postgres.log').open('wb')
        proc = subprocess.Popen([str(pg/'postgres'), '-D', str(tmp/'pgdata'), '-p', str(port), '-k', str(tmp/'socket'), '-c', 'listen_addresses=127.0.0.1', '-c', 'max_connections=30'], env=env, stdout=pglog, stderr=subprocess.STDOUT)
        receipt['owned_postgres_pid'] = proc.pid
        deadline = time.monotonic()+20
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
        db = 'dp_owned_' + uuid.uuid4().hex
        run([pg/'psql', '-h', '127.0.0.1', '-p', str(port), '-U', 'dp_owner', '-d', 'postgres', '-v', 'ON_ERROR_STOP=1', '-c', 'CREATE DATABASE '+db], 'create-db.log')
        env['PANEL_TEST_DATABASE_URL'] = f'postgres://dp_owner@127.0.0.1:{port}/{db}?sslmode=disable'
        run([go, 'test', '-c', '-o', tmp/'e2e.test', './tests/e2e'], 'build.log', 480)
        receipt['test_binary_sha256'] = sha(tmp/'e2e.test')
        receipt['status'] = 'fail'
        # Package-relative migrations; compiled test does not automatically chdir.
        with (out/'test.log').open('wb') as log:
            result = subprocess.run([str(tmp/'e2e.test'), '-test.run=^TestDataPlaneProductReadiness$', '-test.v', '-test.count=1', '-test.timeout=240s'], cwd=ROOT/'tests/e2e', env=env, stdout=log, stderr=subprocess.STDOUT, timeout=260)
        receipt['exit_code'] = result.returncode
        if result.returncode == 0 and '--- PASS: TestDataPlaneProductReadiness' in (out/'test.log').read_text():
            receipt['status'] = 'pass'
        receipt['xray_hash_unchanged'] = sha(xray) == receipt['binaries'][str(xray)]
        if not receipt['xray_hash_unchanged']:
            receipt['status'] = 'fail'
    except Exception as exc:
        receipt['error'] = type(exc).__name__ + ': ' + str(exc)
    finally:
        # A Go timeout/panic can bypass t.Cleanup. Recover only Xray children
        # whose -c path is beneath this invocation's private temporary root.
        recovered = []
        for p in Path('/proc').glob('[0-9]*'):
            try:
                if os.readlink(p/'exe') != str(xray):
                    continue
                argv = (p/'cmdline').read_bytes().split(b'\0')
                if b'-c' not in argv:
                    continue
                config = Path(os.fsdecode(argv[argv.index(b'-c')+1]))
                if not config.is_relative_to(tmp):
                    continue
                pid = int(p.name)
                # pidfd pins the exact process even if its numeric PID is reused.
                import signal
                fd = os.pidfd_open(pid)
                try:
                    signal.pidfd_send_signal(fd, signal.SIGKILL)
                    recovered.append(pid)
                finally:
                    os.close(fd)
            except (OSError, IndexError):
                pass
        receipt['timeout_recovered_owned_core_pids'] = recovered
        if recovered:
            receipt['status'] = 'fail'
        if proc is not None:
            if proc.poll() is None:
                proc.terminate()  # exact child PID, never pkill or process-group kill
                try:
                    proc.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)
            receipt['owned_postgres_exit'] = proc.returncode
        if pglog:
            pglog.close()
        shutil.rmtree(tmp)
        receipt['temporary_directory_removed'] = not tmp.exists()
        receipt['existing_cores_after'] = existing_cores()
        receipt['existing_cores_unchanged'] = all(receipt['existing_cores_after'].get(pid) == val for pid, val in receipt['existing_cores_before'].items())
        if not receipt['existing_cores_unchanged']:
            receipt['status'] = 'fail'
        (out/'receipt.json').write_text(json.dumps(receipt, indent=2)+'\n')
        for p in out.iterdir():
            if p.is_file():
                p.chmod(0o600)
        print(json.dumps({'status': receipt['status'], 'evidence': str(out), 'error': receipt.get('error')}))
    return 0 if receipt['status'] == 'pass' else 1

if __name__ == '__main__':
    raise SystemExit(main())
