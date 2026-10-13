from pathlib import Path
import argparse
import hashlib
import json
import os
import re
import shlex
import shutil
import statistics
import subprocess
import time

B=Path(__file__).resolve().parent
EXAMPLES=B/'src/examples'
ENV=dict(os.environ,GOROOT=str(B/'goroot'),GOPROXY='off')
ENV.pop('OAPI_TREE_CACHE',None)
ENV.pop('OAPI_TREE_TRACE',None)
STRICT=('petstore-expanded/strict/api',['--config=server.cfg.yaml','../../petstore-expanded.yaml'])

def run(case,mode='off',native=False,ordinary=False,trace=False):
    directory,args=case
    env=ENV.copy()
    if mode!='off': env['OAPI_TREE_CACHE']=str(B/mode)
    if trace: env['OAPI_TREE_TRACE']='1'
    if native:
        cmd=[str(B/'oapi-native'),*args]
    else:
        cmd=[str(B/'harness-bin'),'-C',str(EXAMPLES/directory)]
        if ordinary: cmd += ['-native-imports=false','-user-cache-dir',str(B/'empty-user-cache')]
        cmd += ['--',*args]
    t=time.perf_counter()
    p=subprocess.run(cmd,cwd=EXAMPLES/directory,env=env,text=True,capture_output=True)
    elapsed=time.perf_counter()-t
    if p.returncode or (not native and 'err=<nil>' not in p.stdout):
        raise RuntimeError(f'{directory} {mode}: rc={p.returncode}\n{p.stdout}\n{p.stderr}')
    return elapsed,p

def bench():
    rows=[]
    for ordinary in [False,True]:
        run(STRICT,'cache-wire',ordinary=ordinary)
        for i in range(9):
            modes=['off','cache-wire',f'cold-{ordinary}-{i}']
            # Rotate order so one variant does not always run first.
            modes=modes[i%3:]+modes[:i%3]
            for mode in modes:
                if mode.startswith('cold-'):
                    shutil.rmtree(B/mode,ignore_errors=True)
                elapsed,p=run(STRICT,mode,ordinary=ordinary)
                label='cold' if mode.startswith('cold-') else ('warm' if mode!='off' else 'off')
                rows.append(dict(ordinary=ordinary,round=i,mode=label,wall=elapsed))
            print(f'bench ordinary={ordinary} round={i+1}/9',flush=True)
    (B/'bench.json').write_text(json.dumps(rows,indent=2))
    for ordinary in [False,True]:
        for mode in ['off','cold','warm']:
            xs=[r['wall'] for r in rows if r['ordinary']==ordinary and r['mode']==mode]
            print('median',ordinary,mode,round(statistics.median(xs),4),'range',round(min(xs),4),round(max(xs),4),flush=True)

def gate():
    lines=Path('/private/tmp/oapiperf/lines.txt').read_text().splitlines()
    blocks=[]
    for line in Path('/private/tmp/oapiperf/all.native').read_text().splitlines():
        if line.startswith('== '): blocks.append([])
        elif line.startswith('  wrote '): blocks[-1].append(line.split()[1])
    assert len(lines)==len(blocks)==53
    cases=[]
    for line in lines:
        where,args=line.split('://',1)
        directory=str(Path(where.rsplit(':',1)[0]).parent)
        cases.append((directory,shlex.split(args)))
    results=[]
    for i,case in enumerate(cases):
        times={}; manifests={}
        for label,native,mode in [('native',True,'off'),('off',False,'off'),('warm',False,'cache-wire')]:
            elapsed,p=run(case,mode,native=native)
            times[label]=elapsed
            manifests[label]={name:hashlib.sha256((EXAMPLES/case[0]/name).read_bytes()).hexdigest() for name in blocks[i]}
        if manifests['native']!=manifests['off'] or manifests['native']!=manifests['warm']:
            raise RuntimeError(f'output mismatch at {i}: {case}')
        results.append(dict(index=i,case=case,times=times,sha256=manifests['native']))
        (B/'gate.json').write_text(json.dumps(results,indent=2))
        print(f'gate {i+1}/53 {case[0]} identical',flush=True)
    print('gate all 53 identical', {label:round(sum(r['times'][label] for r in results),3) for label in ['native','off','warm']},flush=True)

def representatives():
    results=[]
    for line in Path('/private/tmp/oapiperf/cases.txt').read_text().splitlines():
        parts=shlex.split(line);case=(parts[0],parts[1:])
        for mode in ['off','cache-wire']:
            elapsed,p=run(case,mode,trace=True)
            (B/('trace-'+case[0].replace('/','_')+'-'+mode+'.err')).write_text(p.stderr)
            rows=[dict(re.findall(r'(\w+)=([^\s]+)',line)) for line in p.stderr.splitlines() if line.startswith('TREE_CACHE ')]
            stages={k:sum(float(r.get(k,0)) for r in rows) for k in ['total','read','decode','parse','encode','write','bytes']}
            results.append(dict(case=case,mode=mode,wall=elapsed,files=len(rows),stages=stages))
            print(case[0],mode,round(elapsed,4),flush=True)
    (B/'representatives.json').write_text(json.dumps(results,indent=2))

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('mode',choices=['bench','gate','representatives']);args=parser.parse_args()
    globals()[args.mode]()
