#!/usr/bin/env python3
"""Build checksummed hub-only update assets; publishing is a separate explicit step."""
import argparse,hashlib,json,os,platform,re,shutil,subprocess
from pathlib import Path

def run(args,**kw):subprocess.run(args,check=True,**kw)
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('version');p.add_argument('--source',default=str(Path(__file__).resolve().parents[1]));p.add_argument('--out',required=True);a=p.parse_args()
 if not re.fullmatch(r'\d+\.\d+\.\d+',a.version):raise ValueError('Use a stable semantic hub version')
 source=Path(a.source).resolve();out=Path(a.out).resolve();out.mkdir(parents=True,exist_ok=True)
 for required in ['hub/internal/web/migrate.go','hub/internal/web/move_folders.go','hub/internal/web/hub_update.go']:
  if not (source/required).is_file():raise RuntimeError('Package the deployed migration-service checkout; missing '+required)
 run(['go','test','./...'],cwd=source/'hub')
 manifest={'schema':1,'version':a.version,'platforms':{},'support':{}}
 for system,arch in [('darwin','arm64'),('darwin','amd64'),('linux','arm64'),('linux','amd64')]:
  key=system+'-'+arch;name='hub-'+key;dest=out/name
  env=dict(os.environ,GOOS=system,GOARCH=arch,CGO_ENABLED='0')
  run(['go','build','-trimpath','-ldflags','-s -w -X main.version='+a.version,'-o',str(dest),'.'],cwd=source/'hub',env=env)
  if system=='darwin':
   if platform.system()!='Darwin':raise RuntimeError('Build/sign Darwin hub assets on a Mac')
   run(['codesign','--force','--sign','-','--identifier','com.llm-brain.hub',str(dest)])
   run(['codesign','--verify','--strict',str(dest)])
  manifest['platforms'][key]={'name':name,'sha256':hashlib.sha256(dest.read_bytes()).hexdigest()}
 for name in ['brain-hub-update','brain-mcp']:
  shutil.copy2(source/'bin'/name,out/name)
  manifest['support'][name]={'name':name,'sha256':hashlib.sha256((out/name).read_bytes()).hexdigest()}
 (out/'hub-update.json').write_text(json.dumps(manifest,indent=2)+'\n')
 print(json.dumps(manifest))
if __name__=='__main__':main()
