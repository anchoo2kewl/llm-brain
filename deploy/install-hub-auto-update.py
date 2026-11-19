#!/usr/bin/env python3
"""Install the independent hub updater scheduler (launchd or a user systemd timer)."""
import argparse,json,os,platform,plistlib,shlex,shutil,subprocess,sys
from pathlib import Path

def run(*args,check=True):return subprocess.run(args,check=check,capture_output=True,text=True)
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--brain',default=str(Path(__file__).resolve().parents[1]));p.add_argument('--helper');p.add_argument('--state',default=str(Path.home()/'.local/state/llm-brain'));p.add_argument('--url',default='http://127.0.0.1:8787');p.add_argument('--hub-service',default='llm-brain-hub.service');a=p.parse_args()
 brain=Path(a.brain).resolve();state=Path(a.state).resolve();helper=brain/'bin/brain-hub-update';source=Path(a.helper) if a.helper else Path(__file__).resolve().parents[1]/'bin/brain-hub-update'
 if not (brain/'bin/hub').is_file():raise RuntimeError('Install the hub daemon first')
 helper.parent.mkdir(parents=True,exist_ok=True)
 if source.resolve()!=helper.resolve():shutil.copy2(source,helper)
 helper.chmod(0o755);state.mkdir(parents=True,exist_ok=True,mode=0o700);(state/'logs').mkdir(exist_ok=True)
 config=state/'hub-update.json';cfg=json.loads(config.read_text()) if config.exists() else {}
 cfg.setdefault('auto_enabled',True);cfg.setdefault('interval_hours',6);cfg.update(hub_url=a.url,service=a.hub_service)
 tmp=config.with_suffix('.tmp');tmp.write_text(json.dumps(cfg)+'\n');tmp.chmod(0o600);os.replace(tmp,config)
 args=['/usr/bin/python3',str(helper),'--brain',str(brain),'--state',str(state),'--automatic']
 if platform.system()=='Darwin':
  label='com.llm-brain.hub-update';domain='gui/'+str(os.getuid());dst=Path.home()/'Library/LaunchAgents'/ (label+'.plist');dst.parent.mkdir(parents=True,exist_ok=True)
  job={'Label':label,'ProgramArguments':args,'RunAtLoad':True,'StartInterval':900,'ProcessType':'Background','StandardOutPath':str(state/'logs/hub-update.log'),'StandardErrorPath':str(state/'logs/hub-update.log')}
  run('/bin/launchctl','bootout',domain+'/'+label,check=False);dst.write_bytes(plistlib.dumps(job));run('/bin/launchctl','bootstrap',domain,str(dst))
 elif platform.system()=='Linux':
  unit=Path.home()/'.config/systemd/user';unit.mkdir(parents=True,exist_ok=True)
  command=' '.join(shlex.quote(x.replace('%','%%')) for x in args)
  (unit/'llm-brain-hub-update.service').write_text('[Unit]\nDescription=Update the Brain Hub daemon\n[Service]\nType=oneshot\nExecStart='+command+'\n')
  (unit/'llm-brain-hub-update.timer').write_text('[Unit]\nDescription=Check for Brain Hub daemon updates\n[Timer]\nOnBootSec=5m\nOnUnitActiveSec=15m\nRandomizedDelaySec=60\nPersistent=true\n[Install]\nWantedBy=timers.target\n')
  run('systemctl','--user','daemon-reload');run('systemctl','--user','enable','--now','llm-brain-hub-update.timer')
 else:raise RuntimeError('Hub auto-updates support macOS and Linux')
 print(json.dumps({'installed':True,'auto_enabled':cfg['auto_enabled'],'interval_hours':cfg['interval_hours']}))
if __name__=='__main__':main()
