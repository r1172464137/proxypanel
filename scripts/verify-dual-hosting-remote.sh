#!/usr/bin/env bash
# Disposable dual-HTTPS fixture, never a deployment. Optional DUAL_BASE_REF
# exercises a genuine historical source/migration upgrade; default is HEAD -> HEAD.
set -euo pipefail
umask 077
ROOT=$(cd "$(dirname "$0")/.." && pwd)
export DUAL_ROOT="$ROOT"
export DUAL_HOST=root@192.168.5.203
export DUAL_KEY=${DUAL_KEY:-/home/compile/Work/.keys/db_ed25519}
export DUAL_KNOWN_HOSTS=${DUAL_KNOWN_HOSTS:-/home/compile/Work/.keys/known_hosts}
export DUAL_NODE_MODULES=${DUAL_NODE_MODULES:-/home/compile/Work/proxypanel/web/node_modules}
export DUAL_GO=${DUAL_GO:-/home/compile/sdk/go1.27.3/go/bin/go}
export DUAL_BASE_REF=${DUAL_BASE_REF:-HEAD}
export DUAL_EVIDENCE=${DUAL_EVIDENCE:-/home/compile/Work/proxypanel/.ai/evidence/deployment/dual-hosting/run-$(date -u +%Y%m%dT%H%M%SZ)-$(openssl rand -hex 4)}
mkdir -p "$DUAL_EVIDENCE"; chmod 700 "$DUAL_EVIDENCE"
python3 - <<'PY'
import os,sys,subprocess as sp,pathlib,tempfile,secrets,json,time,socket,tarfile,shutil,hashlib
root=pathlib.Path(os.environ['DUAL_ROOT']); out=pathlib.Path(os.environ['DUAL_EVIDENCE']).resolve()
allowed=pathlib.Path('/home/compile/Work/proxypanel/.ai/evidence/deployment/dual-hosting').resolve()
assert out.is_relative_to(allowed), 'evidence must stay in main private evidence tree'
run='pp-dual-'+secrets.token_hex(8); remote='/tmp/'+run
images={kind:run+':'+kind for kind in ['base','candidate']}
ssh=['ssh','-i',os.environ['DUAL_KEY'],'-o','UserKnownHostsFile='+os.environ['DUAL_KNOWN_HOSTS'],'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','ConnectTimeout=10','-o','ServerAliveInterval=15','-o','ServerAliveCountMax=3']
host=os.environ['DUAL_HOST']; worker=None; tunnel=None; rc=1; created=False
log=open(out/'execution.log','w'); modules=root/'web/node_modules'; linked=False
refs={}; versions={}; artifacts={}; before=None

def remote_cmd(command,**kw):
    kw.setdefault('timeout',120)
    return sp.run(ssh+[host,command],check=True,**kw)
def snapshot(name):
    command="for id in $(docker ps -aq); do docker inspect --format '{{.Id}} {{.Name}} {{.State.StartedAt}} {{.RestartCount}}' \"$id\"; done; docker network ls --no-trunc --format '{{.ID}} {{.Name}}'"
    data=remote_cmd(command,stdout=sp.PIPE).stdout.decode()
    (out/name).write_text(data)
    return set(data.splitlines())
try:
    for kind,ref in [('base',os.environ['DUAL_BASE_REF']),('candidate','HEAD')]:
        refs[kind]=sp.check_output(['git','rev-parse','--verify',ref+'^{commit}'],cwd=root,text=True).strip()
    (out/'refs.json').write_text(json.dumps(refs,indent=2))
    (out/'patch.diff').write_bytes(sp.check_output(['git','diff'],cwd=root))
    before=snapshot('remote-before.txt')
    original_images=set(remote_cmd('docker image ls -q --no-trunc',stdout=sp.PIPE).stdout.decode().splitlines())
    (out/'original-images.txt').write_text('\n'.join(sorted(original_images))+'\n')
    remote_cmd(f'test ! -e {remote} && umask 077 && mkdir -m 700 {remote} && mkdir {remote}/evidence {remote}/bridge {remote}/base {remote}/candidate')
    created=True
    for kind,sha in refs.items():
        with tempfile.TemporaryDirectory(prefix=kind+'-',dir=out) as temp:
            temp=pathlib.Path(temp); source=temp/'source'; source.mkdir()
            (temp/'source.tar').write_bytes(sp.check_output(['git','archive',sha],cwd=root))
            with tarfile.open(temp/'source.tar') as tf: tf.extractall(source,filter='data')
            migrations=sorted((source/'migrations').glob('*.sql'))
            versions[kind]=max(int(f.name.split('_')[0]) for f in migrations)
            (out/(kind+'-migrations.json')).write_text(json.dumps([f.name for f in migrations]))
            # Build the archived source, never the working tree. Require its exact SDK.
            required=next(line.split()[1] for line in (source/'go.mod').read_text().splitlines() if line.startswith('go '))
            go=os.environ['DUAL_GO']
            sdk=sp.check_output([go,'version'],text=True)
            assert ' go'+required+' ' in sdk, (sdk,required)
            binary=source/'bin/amd64/panel'; binary.parent.mkdir(parents=True,exist_ok=True)
            env=os.environ.copy(); env.update(CGO_ENABLED='0',GOOS='linux',GOARCH='amd64',GOTOOLCHAIN='local')
            sp.run([go,'build','-trimpath','-buildvcs=false','-o',str(binary),'./cmd/panel'],cwd=source,env=env,stdout=log,stderr=sp.STDOUT,check=True,timeout=240)
            # Current panel images serve same-release Agent artifacts for the
            # one-line installer. Build both node architectures into the exact
            # archived source context; older Dockerfiles simply ignore them.
            for arch in ('amd64','arm64'):
                agent=source/'bin'/arch/'agent'; agent.parent.mkdir(parents=True,exist_ok=True)
                agent_env=env.copy(); agent_env['GOARCH']=arch
                sp.run([go,'build','-trimpath','-buildvcs=false','-o',str(agent),'./cmd/agent'],cwd=source,env=agent_env,stdout=log,stderr=sp.STDOUT,check=True,timeout=240)
            (out/(kind+'-go-version.txt')).write_bytes(sp.check_output([go,'version','-m',str(binary)]))
            artifacts[kind]={'ref':sha,'panel_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),
                'web_tree':sp.check_output(['git','rev-parse',sha+':web'],cwd=root,text=True).strip(),
                'migrations':{f.name:hashlib.sha256(f.read_bytes()).hexdigest() for f in migrations}}
            (out/'build-artifacts.json').write_text(json.dumps(artifacts,indent=2))
            if kind=='candidate':
                shutil.copy2(root/'scripts/verify-panel-image.sh',source/'scripts/verify-panel-image.sh')
            pack=sp.Popen(['tar','-C',str(source),'-czf','-','.'],stdout=sp.PIPE)
            try: remote_cmd(f'tar -xzf - -C {remote}/{kind}',stdin=pack.stdout,stdout=log,stderr=sp.STDOUT)
            finally: pack.stdout.close()
            assert pack.wait(timeout=30)==0
        remote_cmd(f'cd {remote}/{kind} && timeout 420 docker build --label io.proxypanel.dual-build={run} --label org.opencontainers.image.revision={sha} --build-arg TARGETARCH=amd64 -f deploy/Dockerfile.panel -t {images[kind]} .',stdout=log,stderr=sp.STDOUT,timeout=450)
    hook='''import os,json,time,pathlib
p=pathlib.Path(os.environ['BRIDGE'])
r={k:v for k,v in os.environ.items() if k.startswith('PANEL_')}
t=p/'request.tmp'; t.write_text(json.dumps(r)); t.rename(p/'request.json')
for i in range(200):
    if (p/'result').exists():
        code=int((p/'result').read_text()); (p/'result').unlink(); raise SystemExit(code)
    time.sleep(1)
raise SystemExit('browser bridge timeout')
'''
    remote_cmd(f'cat > {remote}/hook.py',input=hook.encode())
    with socket.socket() as sock: sock.bind(('127.0.0.1',0)); localport=sock.getsockname()[1]
    command=f"cd {remote}/candidate && umask 077 && PANEL_FIXTURE_OWNER={run} PANEL_EXPECT_CURRENT_SCHEMA={versions['base']} PANEL_EXPECT_UPGRADE_SCHEMA={versions['candidate']} PANEL_SMOKE_PUBLIC_URL=https://127.0.0.1:{localport} BRIDGE={remote}/bridge PANEL_EVIDENCE_DIR={remote}/evidence PANEL_BROWSER_HOOK='python3 {remote}/hook.py' timeout --signal=TERM 1000 bash scripts/verify-panel-image.sh {images['base']} {images['candidate']}"
    worker=sp.Popen(ssh+[host,command],stdout=log,stderr=sp.STDOUT)
    if not modules.exists(): modules.symlink_to(os.environ['DUAL_NODE_MODULES'],target_is_directory=True); linked=True
    deadline=time.time()+1100
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
    if worker.poll() is None: raise RuntimeError('overall verification timeout')
    rc=worker.returncode
finally:
    if tunnel: tunnel.terminate(); tunnel.wait(timeout=15)
    if worker and worker.poll() is None:
        # Remote timeout and EXIT trap own teardown; do not remove a live runner's files.
        worker.wait(timeout=240)
    if created:
        with open(out/'remote-evidence.tar.gz','wb') as f:
            remote_cmd(f'tar -czf - -C {remote} evidence',stdout=f,stderr=log)
        with tarfile.open(out/'remote-evidence.tar.gz') as tf: tf.extractall(out/'remote',filter='data')
        # Fallback is exact-owner only, also covers a failure before the fixture trap.
        cleanup=f'''set -eu
for id in $(docker ps -aq --filter label=io.proxypanel.dual-hosting={run}); do
  test "$(docker inspect -f '{{{{index .Config.Labels "io.proxypanel.dual-hosting"}}}}' "$id")" = {run}
  docker rm -fv "$id"
done
for id in $(docker network ls -q --filter label=io.proxypanel.dual-hosting={run}); do
  test "$(docker network inspect -f '{{{{index .Labels "io.proxypanel.dual-hosting"}}}}' "$id")" = {run}
  docker network rm "$id"
done
'''
        for image in images.values():
            cleanup+=f'''if [ "$(docker image inspect -f '{{{{index .Config.Labels "io.proxypanel.dual-build"}}}}' {image} 2>/dev/null)" = {run} ]; then docker image rm {image}; fi\n'''
        cleanup+=f'''docker ps -a --filter label=io.proxypanel.dual-hosting={run}
docker network ls --filter label=io.proxypanel.dual-hosting={run}
docker image ls --filter label=io.proxypanel.dual-build={run}
test -z "$(docker ps -aq --filter label=io.proxypanel.dual-hosting={run})"
test -z "$(docker network ls -q --filter label=io.proxypanel.dual-hosting={run})"
test -z "$(docker image ls -q --filter label=io.proxypanel.dual-build={run})"
rm -rf -- {remote}
'''
        with open(out/'cleanup-receipt.txt','w') as f: remote_cmd(cleanup,stdout=f,stderr=log)
    after=snapshot('remote-after.txt')
    unchanged=before is not None and before==after
    images_preserved=locals().get('original_images',set()) <= set(remote_cmd('docker image ls -q --no-trunc',stdout=sp.PIPE).stdout.decode().splitlines())
    associations=[]
    for mode in ['spa','api']:
        for phase,kind in [('current','base'),('upgrade','candidate')]:
            folder=out/'remote/evidence'/(mode+'-'+phase)
            report=out/(mode+'-'+phase)/'report.json'
            if not (folder/'fingerprints.sha256').exists() or not report.exists(): continue
            fingerprints={line.split()[1]:line.split()[0] for line in (folder/'fingerprints.sha256').read_text().splitlines()}
            browser=json.loads(report.read_text()); asset=browser.get('asset',{})
            matched=(fingerprints.get('panel')==artifacts[kind]['panel_sha256'] and
                     fingerprints.get('dist/'+asset.get('path','').lstrip('/'))==asset.get('sha256') and
                     all(fingerprints.get('migrations/'+name)==digest for name,digest in artifacts[kind]['migrations'].items()) and
                     browser.get('status')=='passed')
            associations.append({'mode':mode,'phase':phase,'ref':refs[kind],'panel_sha256':fingerprints.get('panel'),'browser_asset':asset,'matched':matched})
    (out/'artifact-associations.json').write_text(json.dumps(associations,indent=2))
    if not unchanged or not images_preserved or len(associations)!=4 or not all(a['matched'] for a in associations): rc=1
    if linked: modules.unlink()
    (out/'result.json').write_text(json.dumps({'exit_code':rc,'refs':refs,'expected_schema':versions,'original_container_network_identities_unchanged':unchanged,'original_images_preserved':images_preserved,'remote_dir':remote,'boundary':'No Agent: not Agent-stop proof. Isolated nginx container simulates host TLS; real host nginx/services untouched.'},indent=2))
    log.close()
    for f in out.rglob('*'):
        if f.is_file(): f.chmod(0o600)
        elif f.is_dir(): f.chmod(0o700)
    print('Evidence:',out,'exit:',rc)
sys.exit(rc)
PY
