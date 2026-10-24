#!/usr/bin/env bash
# Authorized disposable remote Docker fixture + local, version-matched Playwright.
# No agent, no push, no real deployment mutation. Run from an isolated candidate worktree.
# Dependencies: local go/node/python3/ssh, main web/node_modules; remote Docker/python3/openssl.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
export DUAL_ROOT="$ROOT"
export DUAL_HOST=${DUAL_HOST:-root@192.168.5.203}
export DUAL_KEY=${DUAL_KEY:-/home/compile/Work/.keys/db_ed25519}
export DUAL_KNOWN_HOSTS=${DUAL_KNOWN_HOSTS:-/home/compile/Work/.keys/known_hosts}
export DUAL_NODE_MODULES=${DUAL_NODE_MODULES:-/home/compile/Work/proxypanel/web/node_modules}
export DUAL_GO=${DUAL_GO:-/home/compile/sdk/go1.27.3/go/bin/go}
export DUAL_EVIDENCE=${DUAL_EVIDENCE:-/home/compile/Work/proxypanel/.ai/evidence/deployment/dual-hosting/run-$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 4)}
mkdir -p "$DUAL_EVIDENCE"; chmod 700 "$DUAL_EVIDENCE"
python3 - <<'PY'
import os,sys,subprocess as sp,pathlib,tempfile,secrets,json,time,socket,tarfile,shutil
root=pathlib.Path(os.environ['DUAL_ROOT']); out=pathlib.Path(os.environ['DUAL_EVIDENCE'])
run='pp-dual-'+secrets.token_hex(8); remote='/tmp/'+run; image=run+':candidate'
ssh=['ssh','-i',os.environ['DUAL_KEY'],'-o','UserKnownHostsFile='+os.environ['DUAL_KNOWN_HOSTS'],'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes']
host=os.environ['DUAL_HOST']; worker=None; tunnel=None; rc=1
log=open(out/'execution.log','w'); created=False
modules=root/'web/node_modules'; linked=False

def remote_cmd(command,**kw): return sp.run(ssh+[host,command],check=True,**kw)
def snapshot(name):
    with open(out/name,'w') as f: remote_cmd("docker ps -a --format '{{.ID}} {{.Names}} {{.Status}} {{.Labels}}'; docker network ls --format '{{.ID}} {{.Name}} {{.Labels}}'",stdout=f)
try:
    sha=sp.check_output(['git','rev-parse','HEAD'],cwd=root,text=True).strip()
    (out/'candidate.txt').write_text(sha+'\n')
    (out/'patch.diff').write_bytes(sp.check_output(['git','diff'],cwd=root))
    snapshot('remote-before.txt')
    remote_cmd(f'test ! -e {remote} && mkdir -m 700 {remote} && mkdir -m 700 {remote}/evidence {remote}/bridge')
    created=True
    with tempfile.TemporaryDirectory(prefix=run+'-') as temp:
        temp=pathlib.Path(temp); source=temp/'source'; source.mkdir()
        archive=sp.check_output(['git','archive','HEAD'],cwd=root)
        (temp/'source.tar').write_bytes(archive)
        with tarfile.open(temp/'source.tar') as tf: tf.extractall(source,filter='data')
        for f in ['scripts/verify-panel-image.sh','web/tests/host-nginx-smoke.mjs']:
            shutil.copy2(root/f,source/f)
        binary=source/'bin/amd64/panel'; binary.parent.mkdir(parents=True)
        env=os.environ.copy(); env.update(CGO_ENABLED='0',GOOS='linux',GOARCH='amd64',GOTOOLCHAIN='local')
        sp.run([os.environ['DUAL_GO'],'build','-trimpath','-buildvcs=false','-o',str(binary),'./cmd/panel'],cwd=root,env=env,stdout=log,stderr=sp.STDOUT,check=True)
        (out/'go-version.txt').write_bytes(sp.check_output([os.environ['DUAL_GO'],'version','-m',str(binary)]))
        pack=sp.Popen(['tar','-C',str(source),'-czf','-','.'],stdout=sp.PIPE)
        remote_cmd(f'tar -xzf - -C {remote}',stdin=pack.stdout,stdout=log,stderr=sp.STDOUT)
        assert pack.wait()==0
    hook='''import os,json,time,pathlib
p=pathlib.Path(os.environ['BRIDGE'])
r={k:v for k,v in os.environ.items() if k.startswith('PANEL_')}
t=p/'request.tmp'; t.write_text(json.dumps(r)); t.rename(p/'request.json')
for i in range(600):
    if (p/'result').exists():
        code=int((p/'result').read_text()); (p/'result').unlink(); raise SystemExit(code)
    time.sleep(1)
raise SystemExit('browser bridge timeout')
'''
    remote_cmd(f"cat > {remote}/hook.py",input=hook.encode())
    with socket.socket() as sock: sock.bind(('127.0.0.1',0)); localport=sock.getsockname()[1]
    command=f"cd {remote} && docker build --label io.proxypanel.dual-build={run} --label org.opencontainers.image.revision={sha} --build-arg TARGETARCH=amd64 -f deploy/Dockerfile.panel -t {image} . && PANEL_SMOKE_PUBLIC_URL=https://127.0.0.1:{localport} BRIDGE={remote}/bridge PANEL_EVIDENCE_DIR={remote}/evidence PANEL_BROWSER_HOOK='python3 {remote}/hook.py' bash scripts/verify-panel-image.sh {image} {image}"
    worker=sp.Popen(ssh+[host,command],stdout=log,stderr=sp.STDOUT)
    if not modules.exists(): modules.symlink_to(os.environ['DUAL_NODE_MODULES'],target_is_directory=True); linked=True
    deadline=time.time()+1300
    while worker.poll() is None and time.time()<deadline:
        req=remote_cmd(f'test ! -f {remote}/bridge/request.json || cat {remote}/bridge/request.json',stdout=sp.PIPE).stdout
        if not req: time.sleep(2); continue
        remote_cmd(f'rm {remote}/bridge/request.json')
        request=json.loads(req); mode=request['PANEL_SMOKE_MODE']; phase=request['PANEL_SMOKE_PHASE']
        target=out/(mode+'-'+phase); target.mkdir(mode=0o700,exist_ok=True)
        tunnel=sp.Popen(ssh+['-o','ExitOnForwardFailure=yes','-N','-L',f'127.0.0.1:{localport}:127.0.0.1:{request["PANEL_SMOKE_PORT"]}',host],stdout=log,stderr=sp.STDOUT)
        time.sleep(1)
        env=os.environ.copy(); env.update(request); env.update(PANEL_BASE=f'https://127.0.0.1:{localport}',PANEL_SMOKE_SELF_SIGNED='1',PANEL_BROWSER_EVIDENCE=str(target),PANEL_BROWSER_STATE=str(out/(mode+'-session.json')))
        try:
            result=sp.run(['node',str(root/'web/tests/host-nginx-smoke.mjs')],env=env,stdout=log,stderr=sp.STDOUT,timeout=180).returncode
        except sp.TimeoutExpired: result=124
        finally:
            tunnel.terminate(); tunnel.wait(timeout=15); tunnel=None
        remote_cmd(f"printf '%s' {result} > {remote}/bridge/result")
    if worker.poll() is None: raise RuntimeError('overall verification timeout; remote EXIT trap must finish cleanup')
    rc=worker.returncode
finally:
    if tunnel: tunnel.terminate(); tunnel.wait(timeout=15)
    if worker and worker.poll() is None:
        # Do not broad-kill remote processes or delete a live fixture's files.
        (out/'cleanup-risk.txt').write_text('Remote runner still active: '+remote+'; wait for bridge timeout/EXIT cleanup.\n')
    elif created:
        with open(out/'remote-evidence.tar.gz','wb') as f:
            remote_cmd(f'tar -czf - -C {remote} evidence',stdout=f,stderr=log)
        with tarfile.open(out/'remote-evidence.tar.gz') as tf: tf.extractall(out/'remote',filter='data')
        remote_cmd(f'''if [ "$(docker image inspect -f '{{{{index .Config.Labels "io.proxypanel.dual-build"}}}}' {image} 2>/dev/null)" = {run} ]; then docker image rm {image}; fi; rm -rf -- {remote}''',stdout=log,stderr=sp.STDOUT)
    snapshot('remote-after.txt')
    if linked: modules.unlink()
    (out/'result.json').write_text(json.dumps({'exit_code':rc,'candidate':locals().get('sha'),'remote_dir':remote,'boundary':'Isolated nginx container simulates host TLS; real host nginx/services untouched.'},indent=2))
    log.close()
    for f in out.rglob('*'):
        if f.is_file(): f.chmod(0o600)
    print('Evidence:',out,'exit:',rc)
sys.exit(rc)
PY
