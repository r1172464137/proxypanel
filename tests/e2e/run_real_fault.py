#!/usr/bin/env python3
"""Three owned physical faults at one source/binary fingerprint; no shared services.
python3 tests/e2e/run_real_fault.py [--evidence-dir ABSOLUTE_NEW_DIRECTORY]
Tools use the same GO_BIN/PG_BIN/PG_LIB defaults as run_data_plane.py.
ENOSPC uses unshare -Urnm + a new 8MiB tmpfs, never fills a host filesystem.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import uuid
from run_data_plane import ROOT, MAIN, sha

TESTS = ['TestRealFaultPostgresOutage', 'TestRealFaultRedisOutage', 'TestRealFaultENOSPC']

def identities():
    result = {}
    for p in Path('/proc').glob('[0-9]*'):
        try:
            exe = os.readlink(p/'exe')
            if Path(exe).name in ('postgres', 'redis-server', 'xray'):
                result[p.name] = {'exe': exe, 'starttime': (p/'stat').read_text().split()[21]}
        except (OSError, IndexError):
            pass
    return result

def fingerprint():
    paths = [*ROOT.rglob('*.go'), *ROOT.joinpath('migrations').glob('*.sql'), ROOT/'go.mod', ROOT/'go.sum', ROOT/'tests/e2e/run_real_fault.py', ROOT/'tests/e2e/run_data_plane.py', ROOT/'scripts/backend-test-manifest.json', ROOT/'scripts/backend-test.py']
    return {str(p.relative_to(ROOT)): sha(p) for p in sorted(paths)}

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--evidence-dir', type=Path)
    args = ap.parse_args()
    os.umask(0o077)
    run_id = time.strftime('%Y%m%dT%H%M%SZ')+'-'+uuid.uuid4().hex[:8]
    out = args.evidence_dir or MAIN/'.ai/evidence/backend-test/real-fault-r1'/run_id
    if not out.is_absolute():
        ap.error('evidence directory must be absolute')
    out.mkdir(parents=True, mode=0o700, exist_ok=False)
    tmp = Path(tempfile.mkdtemp(prefix='pp-real-fault-'))
    env = {k:v for k,v in os.environ.items() if not k.startswith(('PANEL_TEST_', 'PANEL_BROWSER_', 'PP_FAULT_'))}
    go = Path(os.environ.get('GO_BIN','/home/compile/sdk/go1.27.3/go/bin/go'))
    pg = Path(os.environ.get('PG_BIN','/home/compile/sdk/pkg/usr/lib/postgresql/17/bin'))
    redis = Path(os.environ.get('REDIS_BIN','/home/compile/sdk/pkg/usr/bin/redis-server'))
    env['PATH'] = str(go.parent)+':'+env['PATH']
    env['LD_LIBRARY_PATH'] = os.environ.get('PG_LIB','/home/compile/sdk/pkg/usr/lib/x86_64-linux-gnu')
    env['TMPDIR'] = str(tmp/'work'); (tmp/'work').mkdir()
    env.update(PP_FAULT_OWNER=run_id, PP_FAULT_PG_CTL=str(pg/'pg_ctl'), PP_FAULT_PG_DATA=str(tmp/'pgdata'), PP_FAULT_PG_LOG=str(out/'postgres.log'), PP_FAULT_REDIS_BIN=str(redis))
    receipt = {'run_id':run_id,'base_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),'source_files':fingerprint(),'original_objects_before':identities(),'scenarios':{},'skip_count':0,'production_changes':False}
    pg_started = False
    def run(cmd, name, cwd=ROOT, timeout=180):
        with (out/name).open('wb') as log:
            p = subprocess.run([str(x) for x in cmd],cwd=cwd,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=timeout)
        return p.returncode
    def checked(cmd,name,timeout=180):
        if run(cmd,name,timeout=timeout):
            raise RuntimeError(name+' failed; see raw log')
    try:
        receipt['tools'] = {str(p):sha(p) for p in [go, pg/'postgres', pg/'initdb', pg/'pg_ctl', redis]}
        checked([go,'version'],'go-version.log')
        checked([pg/'initdb','-D',tmp/'pgdata','-U','dp_owner','--auth=trust','--encoding=UTF8','--locale=C.UTF-8'],'initdb.log')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0)); port=sock.getsockname()[1]
        (tmp/'socket').mkdir()
        options=f'-p {port} -k {tmp}/socket -c listen_addresses=127.0.0.1 -c max_connections=30'
        env['PP_FAULT_PG_OPTIONS']=options
        checked([pg/'pg_ctl','-D',tmp/'pgdata','-w','-t','20','-l',out/'postgres.log','-o',options,'start'],'pg-start.log')
        pg_started=True
        db='dp_owned_'+uuid.uuid4().hex
        checked([pg/'psql','-h','127.0.0.1','-p',port,'-U','dp_owner','-d','postgres','-v','ON_ERROR_STOP=1','-c','CREATE DATABASE '+db],'create-db.log')
        env['PANEL_TEST_DATABASE_URL']=f'postgres://dp_owner@127.0.0.1:{port}/{db}?sslmode=disable'
        env['PANEL_TEST_DB_ISOLATED']='1'
        checked([go,'test','-c','-o',tmp/'fault.test','./tests/e2e'],'build.log',480)
        receipt['test_binary_sha256']=sha(tmp/'fault.test')
        for test in TESTS:
            cmd=[str(tmp/'fault.test'),'-test.run=^'+test+'$','-test.v','-test.count=1','-test.timeout=100s']
            if test.endswith('ENOSPC'):
                # Probe capability separately: inability to create a namespace
                # is BLOCKED, not a Go test PASS or skip.
                probe=['unshare','-Urnm','sh','-eu','-c','mount --make-rprivate /; mount -t tmpfs -o size=8m,mode=700 tmpfs "$1"; umount "$1"','fault-mount',str(tmp/'fs')]
                (tmp/'fs').mkdir()
                if run(probe,'namespace-probe.log'):
                    receipt['scenarios'][test]={'status':'blocked','reason':'private user/mount namespace tmpfs unavailable'}
                    continue
                env['PP_FAULT_FS']=str(tmp/'fs')
                cmd=['unshare','-Urnm','sh','-eu','-c','mount --make-rprivate /; mount -t tmpfs -o size=8m,mode=700 tmpfs "$PP_FAULT_FS"; trap \'umount "$PP_FAULT_FS"\' EXIT; "$@"','fault-mount',*cmd]
            code=run(cmd,test+'.log',cwd=ROOT/'tests/e2e',timeout=120)
            text=(out/(test+'.log')).read_text()
            skipped='--- SKIP:' in text
            receipt['skip_count']+=text.count('--- SKIP:')
            receipt['scenarios'][test]={'status':'pass' if code==0 and f'--- PASS: {test}' in text and not skipped else 'fail','exit_code':code,'command':cmd,'cwd':str(ROOT/'tests/e2e'),'log':str(out/(test+'.log'))}
    except Exception as exc:
        receipt['error']=type(exc).__name__+': '+str(exc)
    finally:
        # Recover only a timed-out test's Redis with this run's private --dir.
        recovered=[]
        for p in Path('/proc').glob('[0-9]*'):
            try:
                if os.readlink(p/'exe') != str(redis): continue
                argv=(p/'cmdline').read_bytes().split(b'\0')
                if b'--dir' not in argv: continue
                directory=Path(os.fsdecode(argv[argv.index(b'--dir')+1]))
                if not directory.is_relative_to(tmp): continue
                import signal
                fd=os.pidfd_open(int(p.name))
                try: signal.pidfd_send_signal(fd,signal.SIGKILL); recovered.append(p.name)
                finally: os.close(fd)
            except (OSError,IndexError): pass
        receipt['timeout_recovered_owned_redis_pids']=recovered
        if pg_started:
            # Exact private data directory; postgres can have been stopped and
            # restarted by the test, so do not rely on its original PID.
            code=run([pg/'pg_ctl','-D',tmp/'pgdata','-w','-t','20','-m','fast','stop'],'pg-cleanup.log')
            receipt['owned_pg_cleanup_exit']=code
            receipt['owned_pg_absent']=run([pg/'pg_ctl','-D',tmp/'pgdata','status'],'pg-status-after.log')==3
        receipt['source_files_after']=fingerprint()
        receipt['source_stable']=receipt['source_files']==receipt['source_files_after']
        receipt['original_objects_after']=identities()
        receipt['original_objects_unchanged']=all(receipt['original_objects_after'].get(k)==v for k,v in receipt['original_objects_before'].items())
        # Namespace mount disappears even on test timeout. No host mount exists.
        receipt['host_mount_absent']=not os.path.ismount(tmp/'fs')
        shutil.rmtree(tmp)
        receipt['temporary_directory_removed']=not tmp.exists()
        for test in TESTS:
            receipt['scenarios'].setdefault(test,{'status':'blocked','reason':receipt.get('error','setup incomplete')})
        receipt['status']='pass' if all(x['status']=='pass' for x in receipt['scenarios'].values()) and receipt['source_stable'] and receipt['original_objects_unchanged'] and receipt.get('owned_pg_absent') and receipt['host_mount_absent'] and receipt['temporary_directory_removed'] and not recovered else 'fail'
        (out/'receipt.json').write_text(json.dumps(receipt,indent=2)+'\n')
        for p in out.iterdir():
            if p.is_file(): p.chmod(0o600)
        print(json.dumps({'status':receipt['status'],'scenarios':receipt['scenarios'],'skip_count':receipt['skip_count'],'evidence':str(out),'error':receipt.get('error')},indent=2))
    return 0 if receipt['status']=='pass' else 1

if __name__=='__main__':
    raise SystemExit(main())
