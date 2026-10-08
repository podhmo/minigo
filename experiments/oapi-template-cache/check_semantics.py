from pathlib import Path
import os
import argparse
import shutil
import subprocess

SOURCE=Path(__file__).resolve().parent
parser=argparse.ArgumentParser()
parser.add_argument('--work',type=Path,default=Path(os.environ.get('OAPI_EXPERIMENT_WORK',SOURCE/'.work')))
args=parser.parse_args()
B=args.work.resolve()
ENV=dict(os.environ,GOROOT=str(B/'goroot'),GOPROXY='off')
ENV.pop('OAPI_TREE_CACHE',None);ENV.pop('OAPI_TREE_TRACE',None)
outputs={}
def check(name,native,cache=None,trace=False):
    env=ENV.copy()
    if cache is not None:env['OAPI_TREE_CACHE']=str(B/cache)
    if trace:env['OAPI_TREE_TRACE']='1'
    command=[str(B/'semantic-native')] if native else [str(B/'harness-bin'),'-C',str(B/'semantic'),'-pkg','.','-show-output']
    p=subprocess.run(command,env=env,cwd=B/'semantic',text=True,capture_output=True)
    (B/(name+'.out')).write_text(p.stdout);(B/(name+'.err')).write_text(p.stderr)
    assert p.returncode==0,(name,p.stdout,p.stderr)
    assert native or 'err=<nil>' in p.stdout,(name,p.stdout,p.stderr)
    outputs[name]='\n'.join(line for line in p.stdout.splitlines() if not line.startswith('elapsed='))
    assert outputs[name]==outputs.get('semantic-native-off',outputs[name]),(name,outputs)
    print(name,'matches native',flush=True)
    return p

shutil.rmtree(B/'semantic-final-v3',ignore_errors=True)
shutil.rmtree(B/'semantic-minigo-cold-v3',ignore_errors=True)
check('semantic-native-off',True)
check('semantic-native-cold',True,'semantic-final-v3')
check('semantic-native-warm',True,'semantic-final-v3')
check('semantic-minigo-off',False)
check('semantic-minigo-cold',False,'semantic-minigo-cold-v3')
check('semantic-minigo-warm',False,'semantic-minigo-cold-v3')
check('semantic-cross-engine',False,'semantic-final-v3')

shutil.rmtree(B/'semantic-corrupt-v3',ignore_errors=True)
shutil.copytree(B/'semantic-minigo-cold-v3',B/'semantic-corrupt-v3')
victim=next(p for p in (B/'semantic-corrupt-v3').glob('*.tree') if b'independent' in p.read_bytes())
victim.write_bytes(b'corrupt snapshot')
p=check('semantic-corrupt',False,'semantic-corrupt-v3',True)
assert 'status=corrupt' in p.stderr,p.stderr

(B/'unavailable-cache').write_text('not a directory')
p=check('semantic-unavailable',False,'unavailable-cache',True)
assert 'status=uncached' in p.stderr,p.stderr
print('all semantic, corruption and unavailable-cache checks passed',flush=True)
