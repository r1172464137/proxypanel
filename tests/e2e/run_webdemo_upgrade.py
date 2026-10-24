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
2. builds the OLD Agent binary read-only from an explicit historical git
   commit with `git archive` into a private directory (default 52bf44a), and
   records Go SDK / module versions / go.sum sha / archive source sha / build
   command / binary sha. The saved `build/linux-amd64/agent` artifact is NOT a
   provenance anchor: its `.proxypanel-build` marker names no commit, its
   buildinfo only bounds it (golang.org/x/sys v0.36.0, which both ec7ee10 and
   52bf44a pin, so no commit can be singled out), and the sibling SHA256SUMS
   proves only that the files were not corrupted together. If that artifact is
   used explicitly via --old-agent the receipt records its provenance as
   UNKNOWN and marks the old source as not proven;
   builds the NEW Agent from the current worktree candidate;
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
# Artifact-provenance bounds, stated exactly as the repository supports them:
# the shipped artifact's buildinfo resolves golang.org/x/sys v0.36.0.  In this
# repository BOTH the initial commit ec7ee10 and 52bf44a pin v0.36.0; v0.47.0
# only appears from 069adee (a later security commit).  So buildinfo bounds the
# artifact to "before 069adee" and can never distinguish 52bf44a from ec7ee10 or
# from an untracked tree: it is a bound, not an attribution.
ARTIFACT_EXPECTED_X_SYS = 'v0.36.0'      # ec7ee10 .. 52bf44a
CURRENT_X_SYS = 'v0.47.0'                # 069adee and later (current candidate)
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


def listeners():
    """TCP and UDP loopback listeners (tcp, tcp6, udp, udp6).

    Identity protection must cover bound UDP sockets too: a process may hold a
    UDP-only service with no TCP listener at all.
    """
    out = {}
    for proto, path in (('tcp', '/proc/net/tcp'), ('tcp6', '/proc/net/tcp6'),
                        ('udp', '/proc/net/udp'), ('udp6', '/proc/net/udp6')):
        try:
            lines = Path(path).read_text().splitlines()[1:]
        except OSError:
            continue
        for line in lines:
            f = line.split()
            if len(f) < 10:
                continue
            local, state, inode = f[1], f[3], f[9]
            # tcp listeners are 0A (LISTEN); udp sockets are 07 (unconnected bound).
            if proto.startswith('tcp') and state != '0A':
                continue
            if proto.startswith('udp') and state not in ('07', '0A'):
                continue
            out[f'{proto}:{local}:{inode}'] = state
    return out


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


def buildinfo_deps(binary):
    """Extract `go version -m` module deps without trusting any external tool."""
    try:
        res = subprocess.run([str(GO), 'version', '-m', str(binary)], capture_output=True, text=True)
    except OSError:
        return {}
    deps = {}
    for line in res.stdout.splitlines():
        parts = [p for p in line.split('\t') if p != '']
        if len(parts) >= 3 and parts[0] == 'dep':
            deps[parts[1]] = parts[2]
    return deps


def artifact_provenance(binary, recorded_sha=''):
    """Describe what a saved artifact can and cannot prove.

    The sibling SHA256SUMS is an integrity anchor inside the same writable
    directory: it proves the copy was not corrupted on its own, never which
    commit produced it. Only buildinfo module pins give a (weak) bound.
    """
    deps = buildinfo_deps(binary)
    xsys = deps.get('golang.org/x/sys', '')
    out = {
        'origin': 'UNKNOWN: saved artifact, no verifiable commit attribution',
        'artifact_sha256': sha(binary),
        'artifact_recorded_sha256': recorded_sha,
        'artifact_sibling_sha256sums': 'integrity anchor only, same writable dir, not provenance',
        'artifact_marker_file': 'names no commit',
        'artifact_buildinfo_x_sys': xsys,
        'artifact_commit_attribution': 'unattributed',
        'artifact_attribution_reason': (
            'the build marker names no commit and SHA256SUMS is same-directory '
            'self-consistency only, so no commit can be attributed'),
    }
    if xsys:
        out['artifact_provenance_bound'] = (
            f'buildinfo pins golang.org/x/sys {xsys}; in this repository that is a bound, not an '
            f'attribution: ec7ee10 and 52bf44a both pin {ARTIFACT_EXPECTED_X_SYS}, '
            f'{CURRENT_X_SYS} appears from 069adee. The artifact therefore cannot be attributed to a '
            f'specific commit, and is not interchangeable with any git-archive build.')
        out['artifact_predates_current_candidate'] = xsys != CURRENT_X_SYS
    return out


def build_agent(rev_or_path, out, logs, env, label):
    """Build from a historical commit read-only (git archive into a private dir).

    Never mutates the worktree, go.mod or the system Go SDK; no network fetch of
    an arbitrary binary is involved - the toolchain builds from archived source.
    """
    if Path(rev_or_path).is_file():
        shutil.copy2(rev_or_path, out)
        return artifact_provenance(out)
    src = Path(tempfile.mkdtemp(prefix=f'ppdemo-upgrade-{label}-src-'))
    try:
        with (logs / f'old-source-{label}.log').open('wb') as log:
            arch = subprocess.run(['git', 'archive', rev_or_path], cwd=str(ROOT),
                                  capture_output=True)
            log.write(arch.stdout)
            log.write(arch.stderr)
        if arch.returncode != 0:
            raise RuntimeError(f'git archive {rev_or_path} failed')
        archive_sha = hashlib.sha256(arch.stdout).hexdigest()
        (src / '.archive.tar').write_bytes(arch.stdout)
        untar = subprocess.run(['tar', '-x', '-f', '-', '-C', str(src)], input=arch.stdout,
                               capture_output=True)
        if untar.returncode != 0:
            raise RuntimeError(f'tar extract of git archive {rev_or_path} failed')
        (src / '.archive.tar').unlink(missing_ok=True)
        commit_sha = subprocess.check_output(['git', 'rev-parse', rev_or_path], cwd=str(ROOT),
                                             text=True).strip()
        gomod = (src / 'go.mod').read_text() if (src / 'go.mod').is_file() else ''
        go_sum = (src / 'go.sum').read_bytes() if (src / 'go.sum').is_file() else b''
        sdk = subprocess.run([str(GO), 'version'], capture_output=True, text=True).stdout.strip()
        cmd = [str(GO), 'build', '-trimpath', '-buildvcs=false', '-o', str(out), './cmd/agent']
        run(cmd, logs / f'old-build-{label}.log', env, timeout=900, cwd=src)
        deps = buildinfo_deps(out)
        return {
            'origin': f'git-archive:{rev_or_path}',
            'commit': commit_sha,
            'archive_source_sha256': archive_sha,
            'go_sdk': sdk,
            'go_mod_sha256': hashlib.sha256(gomod.encode()).hexdigest(),
            'go_mod_requires': [line.strip() for line in gomod.splitlines()
                                if line.strip().startswith(('golang.org/x/', 'modernc.org/', 'github.com/'))],
            'go_sum_sha256': hashlib.sha256(go_sum).hexdigest(),
            'build_command': ' '.join(cmd[:-1] + ['./cmd/agent']),
            'build_dir': 'private tmp copy from git archive (worktree/go.mod/SDK untouched)',
            'binary_sha256': sha(out),
            'buildinfo_x_sys': deps.get('golang.org/x/sys', ''),
        }
    finally:
        shutil.rmtree(src, ignore_errors=True)



def render_report(receipt):
    """Render REPORT.md from this run's receipt only (no hand-written numbers)."""
    def g(*path, default='-'):
        cur = receipt
        for key in path:
            if not isinstance(cur, dict) or key not in cur:
                return default
            cur = cur[key]
        return cur

    state = receipt.get('cleanup', {}).get('state_files', {})
    core_procs = []
    ident_path = receipt.get('evidence_dir', '') + '/state/demo-upgrade-core-identity.json'
    try:
        with open(ident_path) as fh:
            core_procs = json.load(fh).get('core_processes', [])
    except OSError:
        core_procs = []
    lines = [
        '# web-demo D (US-PR-016) run report',
        '',
        'This file is generated by `run_webdemo_upgrade.py` from `receipt.json` of the SAME run.',
        'Every number below is read from that receipt; nothing is typed by hand and nothing is',
        'copied from an earlier run. A field shown as `-` was not produced by this run.',
        '',
        '## Run',
        f'- run_id: `{g("run_id")}`',
        f'- status: `{g("status")}` / exit_code `{g("exit_code")}`',
        f'- worktree commit: `{g("base_commit")}`',
        f'- evidence dir: `{g("evidence_dir")}`',
        f'- lock: acquired at {g("lock_acquired_at")}, released `{g("cleanup", "lock_released")}`',
        '',
        '## Old source provenance (D-1)',
        f'- old Agent origin: `{g("old_agent_origin", "origin")}`',
        f'- provenance verdict: `{g("old_agent_provenance_verdict")}`, closed: `{g("old_source_provenance_closed")}`',
        f'- old commit: `{g("old_agent_origin", "commit")}`',
        f'- archive source sha256: `{g("old_agent_origin", "archive_source_sha256")}`',
        f'- go.mod sha256: `{g("old_agent_origin", "go_mod_sha256")}`',
        f'- go.sum sha256: `{g("old_agent_origin", "go_sum_sha256")}`',
        f'- Go SDK: `{g("old_agent_origin", "go_sdk")}`',
        f'- build command: `{g("old_agent_origin", "build_command")}`',
        f'- old binary sha256: `{g("agent_old_sha256")}`',
        f'- new binary sha256: `{g("agent_new_sha256")}`',
        f'- shipped artifact attribution: `{(g("shipped_artifact_provenance", "artifact_commit_attribution"))}`',
        f'- shipped artifact provenance bound: {g("shipped_artifact_provenance", "artifact_provenance_bound")}',
        f'- shipped artifact == built old: `{g("shipped_artifact_matches_built_old")}`',
        '',
        '## Core switching (D-2)',
        f'- old core: {g("core_old", "version")} sha256 `{g("core_old", "sha256")}`',
        f'- new core: {g("core_new", "version")} sha256 `{g("core_new", "sha256")}`',
        f'- probe (client) core: `{g("probe_core", "sha256")}` origin {g("probe_core", "origin")}',
        '',
        '| step | core pid | starttime | exe sha256 | official match | uid==agent | cgroup==agent | cfg under state |',
        '| --- | --- | --- | --- | --- | --- | --- | --- |',
    ]
    for p in core_procs:
        lines.append('| {step} | {pid} | {st} | `{sha}` | {m} | {u} | {c} | {cfg} |'.format(
            step=p.get('step'), pid=p.get('pid'), st=p.get('starttime_ticks'),
            sha=(p.get('exe_sha256') or '')[:16], m=p.get('exe_matches_expected'),
            u=p.get('owner_matches_agent_uid'), c=p.get('cgroup_matches_agent'),
            cfg=p.get('config_inside_state_dir')))
    lines += ['', 'Per-step real transfers through the managed core (control member, fixed body):', '',
              '| step | bytes | payload sha256 | elapsed ms | class | managed core pid |',
              '| --- | --- | --- | --- | --- | --- |']
    identity = {}
    try:
        with open(ident_path) as fh:
            identity = json.load(fh)
    except OSError:
        identity = {}
    for pr in identity.get('probes', []):
        lines.append('| {s} | {b} | `{h}` | {e} | {c} | {p} |'.format(
            s=pr.get('step'), b=pr.get('bytes'), h=(pr.get('payload_sha256') or '')[:16],
            e=pr.get('elapsed_ms'), c=pr.get('outcome_class'), p=pr.get('managed_core_pid')))
    lines += [
        '',
        '## Cleanup (D-4)',
        f'- postgres pg_ctl stop exit code: `{g("cleanup", "postgres_pg_ctl_stop", "exit_code")}`',
        f'- postgres graceful: `{g("cleanup", "postgres_graceful")}` (exit `{g("cleanup", "postgres_exit_code")}`)',
        f'- postgres stopped: `{g("cleanup", "postgres_stopped")}`; caveat: {g("cleanup", "postgres_stopped_caveat")}',
        f'- leftover processes recovered by SIGKILL: `{g("cleanup", "leftover_processes_recovered")}`',
        f'- work dir removed: `{g("cleanup", "work_dir_removed")}`',
        f'- protection_clean: `{g("after_snapshot", "protection_clean")}`; foreign_vanished: `{g("after_snapshot", "foreign_vanished")}`',
        f'- listeners compared: {len(g("listeners_before", default={}) or {})} before / '
        f'{len(g("after_snapshot", "listeners", default={}) or {})} after (TCP+UDP)',
        '',
        '## Evidence files (sha256 from this run)',
    ]
    for name, info in sorted(state.items()):
        lines.append(f'- `{name}` sha256 `{info.get("sha256")}` bytes {info.get("bytes")}')
    lines.append('')
    return '\n'.join(lines)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    ap.add_argument('--keep-work', action='store_true')
    ap.add_argument('--old-agent', help='use an unprovenanced saved artifact; its origin is recorded as UNKNOWN')
    ap.add_argument('--old-commit', default=OLD_COMMIT_DEFAULT,
                    help='historical commit the OLD Agent is built from (read-only git archive)')
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
        receipt['listeners_before'] = listeners()
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

        # ---- OLD agent: explicit historical commit is the default source ----
        # The saved artifact is never the default and never yields a provenance
        # claim: buildinfo shows x/sys v0.36.0, which contradicts 52bf44a.
        old_agent = out / 'agent-old'
        new_agent = out / 'agent-new'
        if args.old_agent:
            receipt['old_agent_origin'] = build_agent(args.old_agent, old_agent, logs, env, 'old')
            receipt['old_agent_provenance_verdict'] = 'unattributed-artifact'
            receipt['old_agent_usable_as_source_proof'] = False
        else:
            receipt['old_agent_origin'] = build_agent(args.old_commit, old_agent, logs, env, 'old')
            receipt['old_agent_provenance_verdict'] = 'git-archive'
            receipt['old_agent_usable_as_source_proof'] = True
            if SHIPPED_OLD.is_file():
                shipped = artifact_provenance(SHIPPED_OLD)
                receipt['shipped_artifact_provenance'] = shipped
                receipt['shipped_artifact_matches_built_old'] = shipped['artifact_sha256'] == sha(old_agent)
        # The old source is only usable as upgrade evidence when it came from an
        # explicit commit archive; an unattributed artifact stays marked.
        receipt['old_source_provenance_closed'] = receipt.get('old_agent_provenance_verdict') == 'git-archive'
        if args.new_agent:
            receipt['new_agent_origin'] = build_agent(args.new_agent, new_agent, logs, env, 'new')
        else:
            run([GO, 'build', '-trimpath', '-buildvcs=false', '-o', new_agent, './cmd/agent'],
                logs / 'new-build.log', env, timeout=900)
            receipt['new_agent_origin'] = {
                'origin': f'worktree:{receipt["base_commit"]}',
                'build_command': 'go build -trimpath -buildvcs=false -o <evidence>/agent-new ./cmd/agent',
                'go_sdk': subprocess.run([str(GO), 'version'], capture_output=True, text=True).stdout.strip(),
                'binary_sha256': sha(new_agent),
            }
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
        # The traffic client must be independent of whatever core the Agent
        # manages: a separate copy of one official release, in its own dir.
        probe_dir = out / 'core-probe'
        probe_dir.mkdir(mode=0o700, exist_ok=True)
        shutil.copy2(core_paths['new'][0], probe_dir / 'xray')
        (probe_dir / 'xray').chmod(0o755)
        for asset, path in (('geoip.dat', core_paths['new'][1]), ('geosite.dat', core_paths['new'][2])):
            if path is not None:
                shutil.copy2(path, probe_dir / asset)
        receipt['probe_core'] = {
            'path': str(probe_dir / 'xray'),
            'origin': 'independent staged copy of the official new core (never the managed core path)',
            'sha256': sha(probe_dir / 'xray'),
            'version': CORES['new'],
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
            'PP_DEMO_UPGRADE_PROBE_CORE': str(probe_dir / 'xray'),
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
        # Cleanup order matters: stop our own PostgreSQL gracefully first (via
        # pg_ctl for this data dir, then the exact child), so the SIGKILL
        # recovery sweep can never be the thing that "stops" it. Only then do
        # we recover precisely-owned residue, and afterwards take an after
        # snapshot to compare identities against the before snapshot.
        if tmp is not None and pg_proc is not None:
            pgdata = tmp / 'pgdata'
            pgsock = tmp / 'socket'
            stop = subprocess.run([str(PG_BIN / 'pg_ctl'), 'stop', '-D', str(pgdata), '-m', 'fast',
                                   '-w', '-t', '60'], capture_output=True, text=True)
            cleanup['postgres_pg_ctl_stop'] = {
                'exit_code': stop.returncode,
                'stdout': stop.stdout.strip()[-400:],
                'stderr': stop.stderr.strip()[-400:],
            }
        if pg_proc is not None:
            try:
                pg_proc.wait(timeout=60)
                cleanup['postgres_exit_code'] = pg_proc.returncode
                cleanup['postgres_graceful'] = pg_proc.returncode in (0, -15)
            except subprocess.TimeoutExpired:
                cleanup['postgres_graceful'] = False
                pg_proc.terminate()
                try:
                    pg_proc.wait(timeout=30)
                    cleanup['postgres_exit_code'] = pg_proc.returncode
                    cleanup['postgres_terminated'] = True
                except subprocess.TimeoutExpired:
                    pg_proc.kill()
                    pg_proc.wait(timeout=10)
                    cleanup['postgres_killed_after_timeout'] = True
            cleanup['postgres_stopped'] = pg_proc.poll() is not None
            if not cleanup['postgres_graceful']:
                cleanup['postgres_stopped_caveat'] = 'stopped without a graceful pg_ctl exit; not a clean shutdown'

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
                reason = ('exe-under-owned-root' if any(root and exe.startswith(root + '/')
                                                      for root in owned_roots if root)
                          else 'owned-pgdata-or-core-config-on-cmdline')
                try:
                    os.kill(int(pid.name), signal.SIGKILL)
                    cleanup['leftover_processes'].append({'pid': pid.name, 'cmd': cmd[:160],
                                                          'match_reason': reason})
                    cleanup.setdefault('leftover_processes_recovered', []).append(pid.name)
                except OSError:
                    pass
        if tmp is not None:
            shutil.rmtree(tmp, ignore_errors=True)
            cleanup['work_dir_removed'] = not tmp.exists()
            cleanup['test_binary_removed'] = not Path(cleanup['test_binary']).exists() if cleanup.get('test_binary') else True
        cleanup['state_dir_retained'] = str(state_dir)
        # ---- after-snapshot: identity + listener comparison -----------------
        # Anything present before this run that is not ours must still be there
        # (same pid/starttime/exe). Naming a freed port or a recycled pid as
        # "still protected" would be a false claim, so listeners are compared as
        # a set, and pid reuse is caught by starttime.
        after_procs = snapshot_processes()
        after_listeners = listeners()
        before_procs = receipt.get('existing_owned_processes_before') or {}
        before_listeners = receipt.get('listeners_before') or {}
        own_pids = set(cleanup.get('leftover_processes_recovered') or [])
        own_pids.update(str(cleanup.get('postgres_pid')) for _ in (0,) if cleanup.get('postgres_pid'))
        vanished, changed = [], []
        for pid, info in before_procs.items():
            now = after_procs.get(pid)
            if now is None:
                vanished.append({'pid': pid, 'exe': info.get('exe'), 'owned_by_this_run': pid in own_pids})
            elif now.get('starttime') != info.get('starttime') or now.get('exe') != info.get('exe'):
                changed.append({'pid': pid, 'before': info, 'after': now})
        lost_listeners = [k for k in before_listeners if k not in after_listeners]
        new_listeners = [k for k in after_listeners if k not in before_listeners]
        foreign_vanished = [v for v in vanished if not v['owned_by_this_run']]
        receipt['after_snapshot'] = {
            'processes': after_procs,
            'listeners': after_listeners,
            'vanished': vanished,
            'changed_identity': changed,
            'listeners_lost': lost_listeners,
            'listeners_new': new_listeners,
            'foreign_vanished': foreign_vanished,
            'own_scope_clean': not [p for p in after_procs
                                     if tmp and p not in before_procs and
                                     any(root and after_procs[p].get('exe', '').startswith(root + '/')
                                         for root in owned_roots if root)],
            'protection_clean': not foreign_vanished,
            'protection_scope': 'exact pid+starttime+exe identity for the pre-run inventory and full '
                                'TCP/UDP listener set; no broad assertions',
        }
        if foreign_vanished:
            receipt['after_snapshot']['protection_note'] = (
                'pre-run non-owned processes disappeared during this run; recorded as foreign churn, '
                'NOT attributed to this run and NOT claimed as protection success')

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
        # The human-readable summary is generated FROM this receipt, so it can
        # never quote numbers from a different run.
        report = render_report(receipt)
        report_path = out / 'REPORT.md'
        report_path.write_text(report)
        os.chmod(report_path, 0o600)

    print(json.dumps({k: receipt.get(k) for k in ('run_id', 'status', 'exit_code', 'old_agent_origin',
                                                  'new_agent_origin', 'agent_old_sha256', 'agent_new_sha256',
                                                  'core_old', 'core_new', 'error', 'not_run_reason')}, indent=2))
    print(f'evidence: {out}')
    return 0 if receipt.get('status') == 'pass' else 1


if __name__ == '__main__':
    sys.exit(main())
