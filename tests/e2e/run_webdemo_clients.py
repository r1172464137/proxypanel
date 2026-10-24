#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""web-demo group E: owned real-client compatibility runner.

    uv run tests/e2e/run_webdemo_clients.py [--evidence-dir ABS]
                                            [--only vision|cross|matrix]

Runs only this group's own selections against the pinned official cores under
the shared local-runtime flock. It never touches an existing panel, Agent lab,
preview stack or database, and it never rewrites the pinned binaries.

The 21-cell local client matrix is executed from internal/panel/clientconfig
(the build-tagged corematrix selection) with the same three pinned cores; the
REALITY/Vision cells come from tests/e2e/webdemo_clients_vision_test.go. Both
selections write per-cell JSON so the report can point at exact evidence.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[2]
MAIN = Path('/home/compile/Work/proxypanel')
LOCK = Path('/home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock')
DEFAULT_EVIDENCE = MAIN / '.ai/evidence/web-demo/E'

SELECTIONS = {
    # Vision traffic/stats/stop plus the REALITY cross-client rows.
    'vision': {
        'packages': ['./tests/e2e'],
        'run': '^(TestWebDemoVisionTrafficStatsAndStop|TestWebDemoRealityCrossClient)$',
        'tags': [],
        'cells': 3,
    },
    # The reused 21-cell local matrix (20 supported + 1 declared rejection).
    'matrix': {
        'packages': ['./internal/panel/clientconfig'],
        'run': '^TestLocalCore',
        'tags': ['corematrix'],
        'cells': 21,
    },
}


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def lock_is_held():
    """Fail closed unless this process family already holds the shared flock.

    The runner must be started as
        flock -w600 /home/compile/Work/.locks/proxypanel-webdemo-local-runtime.lock \
            uv run tests/e2e/run_webdemo_clients.py
    so the snapshot-to-cleanup window is covered. Authoring never holds it.
    """
    LOCK.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(LOCK, os.O_RDWR | os.O_CREAT, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return True
    finally:
        os.close(fd)
    return False


def find_go():
    return Path(os.environ.get('GO_BIN', '/home/compile/sdk/go1.27.3/go/bin/go'))


def cores():
    return {
        'XRAY_BIN': Path(os.environ.get('XRAY_BIN', '/home/compile/Work/bin/xray')),
        'SING_BOX_BIN': Path(os.environ.get('SING_BOX_BIN', '/home/compile/Work/bin/sing-box')),
        'MIHOMO_BIN': Path(os.environ.get('MIHOMO_BIN', '/home/compile/Work/bin/mihomo')),
    }


def version_of(binary, args):
    out = subprocess.run([str(binary), *args], capture_output=True, text=True, check=True)
    return out.stdout.strip().splitlines()[0] if out.stdout.strip() else ''


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    ap.add_argument('--only', choices=sorted(SELECTIONS), action='append')
    ap.add_argument('--keep-temp', action='store_true')
    args = ap.parse_args()

    run_id = time.strftime('%Y%m%dT%H%M%SZ') + '-' + uuid.uuid4().hex[:8]
    out = (args.evidence_dir or DEFAULT_EVIDENCE / run_id)
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.umask(0o077)

    receipt = {
        'schema': 'webdemo-clients-evidence/v1',
        'run_id': run_id,
        'group': 'E',
        'status': 'not_run',
        'mode': 'owned loopback real cores; no existing panel/Agent/preview/database touched',
        'source_root': str(ROOT),
        'lock': str(LOCK),
    }
    go = find_go()
    binary_paths = cores()
    for name, path in [('go', go), *binary_paths.items()]:
        if not Path(path).is_file():
            receipt['status'] = 'blocked'
            receipt['error'] = f'missing tool {name}: {path}'
            (out / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
            print(json.dumps(receipt, indent=2))
            return 2

    receipt['base_commit'] = subprocess.check_output(
        ['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    if not lock_is_held():
        receipt['status'] = 'blocked'
        receipt['error'] = ('shared local-runtime flock is not held; run the wrapper as '
                            'flock -w600 ' + str(LOCK) + ' uv run tests/e2e/run_webdemo_clients.py')
        (out / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
        print(json.dumps(receipt, indent=2))
        return 2
    receipt['git_status'] = subprocess.check_output(
        ['git', 'status', '--porcelain'], cwd=ROOT, text=True).strip().splitlines()
    receipt['binaries'] = {
        str(path): {'sha256': sha(path), 'version': version_of(path, ['version'] if name == 'XRAY_BIN'
                                                              else (['-v'] if name == 'MIHOMO_BIN' else ['version']))}
        for name, path in binary_paths.items()
    }
    receipt['source_files'] = {
        str(p.relative_to(ROOT)): sha(p)
        for p in [
            ROOT / 'tests/e2e/webdemo_clients_vision_test.go',
            ROOT / 'tests/e2e/run_webdemo_clients.py',
            ROOT / 'tests/e2e/webdemo_clients_manifest.json',
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
        if key.startswith('PANEL_TEST_') or key.startswith('PANEL_BROWSER_') or key.startswith('PANEL_DOCKER'):
            env.pop(key)
    for name, path in binary_paths.items():
        env[name] = str(path)
    # This stage pins Mihomo 1.19.33 explicitly; the historical default in the
    # corematrix helper is 1.19.32. The binaries themselves are not modified.
    env.setdefault('MIHOMO_BIN_VERSION', '1.19.33')
    receipt['pins'] = {
        'XRAY_BIN_VERSION': env.get('XRAY_BIN_VERSION', '26.3.27 (helper default)'),
        'SING_BOX_BIN_VERSION': env.get('SING_BOX_BIN_VERSION', '1.14.3 (helper default)'),
        'MIHOMO_BIN_VERSION': env['MIHOMO_BIN_VERSION'],
    }
    env['TMPDIR'] = str(tmp / 'work')
    (tmp / 'work').mkdir(mode=0o700)
    receipt['env_scrubbed'] = 'PANEL_TEST_/PANEL_BROWSER_/PANEL_DOCKER_* not inherited'

    selected = args.only or ['vision', 'matrix']
    results = {}
    try:
        # The lock covers host-snapshot reuse through cleanup; it is taken only
        # for the run, never for authoring.
        receipt['lock_acquired_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        for name in selected:
            spec = SELECTIONS[name]
            log = out / f'{name}.log'
            cmd = [str(go), 'test', '-count=1', '-p=1', '-timeout=600s', '-v',
                   '-run', spec['run']]
            for tag in spec['tags']:
                cmd += ['-tags', tag]
            cmd += spec['packages']
            with log.open('wb') as fh:
                proc = subprocess.run(cmd, cwd=ROOT, env=env, stdout=fh,
                                      stderr=subprocess.STDOUT, timeout=900)
            text = log.read_text(errors='replace')
            cells = [json.loads(line.split('webdemo_cell=', 1)[1])
                     for line in text.splitlines()
                     if 'webdemo_cell=' in line]
            matrix_cells = [json.loads(line.split('matrix_cell=', 1)[1])
                            for line in text.splitlines()
                            if 'matrix_cell=' in line]
            results[name] = {
                'command': cmd,
                'exit_code': proc.returncode,
                'log': str(log),
                'expected_cells': spec['cells'],
                'observed_vision_cells': cells,
                'observed_matrix_cells': matrix_cells,
                'passed': proc.returncode == 0 and '--- PASS' in text,
            }
            receipt['status'] = 'pass' if results[name]['passed'] else 'fail'
            if not results[name]['passed']:
                break
        receipt['results'] = results
    except Exception as exc:  # noqa: BLE001 - receipt must record the blocker
        receipt['status'] = 'fail'
        receipt['error'] = f'{type(exc).__name__}: {exc}'
    finally:
        # Recover only cores whose config path is under this run's tempdir.
        recovered = []
        proc_root = Path('/proc')
        for pid in proc_root.glob('[0-9]*'):
            try:
                cmdline = (pid / 'cmdline').read_bytes().split(b'\0')
                exe = os.readlink(pid / 'exe')
            except OSError:
                continue
            if Path(exe).name not in {'xray', 'sing-box', 'mihomo'}:
                continue
            if any(str(tmp).encode() in part for part in cmdline):
                try:
                    os.kill(int(pid.name), 15)
                    recovered.append({'pid': pid.name, 'exe': exe})
                except OSError:
                    pass
        receipt['recovered_cores'] = recovered
        if args.keep_temp:
            receipt['tempdir_kept'] = str(tmp)
        else:
            shutil.rmtree(tmp, ignore_errors=True)
            receipt['tempdir_removed'] = True
        receipt['finished_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        (out / 'receipt.json').write_text(json.dumps(receipt, indent=2) + '\n')
        os.chmod(out / 'receipt.json', 0o600)
        print(json.dumps({k: receipt[k] for k in ('run_id', 'status', 'results', 'error')
                          if k in receipt}, indent=2))
    return 0 if receipt['status'] == 'pass' else 1


if __name__ == '__main__':
    sys.exit(main())
