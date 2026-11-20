"""GitHub CLI adapter for managed agents: advisory reviews and mandatory PR labels."""
import json,os,pathlib,re,shutil,subprocess,sys

def validate(args,stdin=''):
 if 'pr' in args:
  i=args.index('pr')
  if args[i+1:i+2]==['review']:
   if any(a in ('--approve','-a','--request-changes','-r') or a.startswith(('--approve=','--request-changes=')) or re.fullmatch(r'-[acrb]+',a) and ('a' in a or 'r' in a) for a in args):raise ValueError('Agents cannot approve PRs or request changes; use advisory comments.')
 if 'api' not in args:return
 text=' '.join(args)+' '+stdin
 if re.search(r'(?:event|state)\s*[=:]\s*["\x27]?(?:APPROVE|REQUEST_CHANGES)\b',text,re.I):raise ValueError('Agent reviews must use COMMENT.')
 if any(re.search(r'/pulls/\d+/reviews(?:/\d+/events)?$',a) for a in args):
  # An API review write must be demonstrably COMMENT. Read-only GETs remain usable.
  write=any(a in ('-f','-F','--field','--raw-field','--input') or a.startswith(('--field=','--raw-field=','--input=')) for a in args) or any(a in ('POST','PATCH','PUT') for a in args)
  body_update='PUT' in args and any(re.search(r'/reviews/\d+$',a) for a in args)
  if write and not body_update:
   try:event=json.loads(stdin).get('event')
   except (ValueError,AttributeError):event=None
   if event!='COMMENT' and not any(a=='event=COMMENT' for a in args):raise ValueError('Agent review API writes require event=COMMENT.')
 if re.search(r'\bmutation\b',text) and re.search(r'(?:add|submit)PullRequestReview\b',text):
  if not re.search(r'event\s*:\s*COMMENT\b',text):raise ValueError('Agent GraphQL reviews require explicit COMMENT.')

def main():
 args=sys.argv[1:];data=None
 if '--input' in args or any(a.startswith('--input=') for a in args):
  value=args[args.index('--input')+1] if '--input' in args else next(a.split('=',1)[1] for a in args if a.startswith('--input='))
  data=sys.stdin.read() if value=='-' else pathlib.Path(value).read_text()
 try:validate(args,data or '')
 except ValueError as e:print(str(e),file=sys.stderr);return 2
 own=pathlib.Path(__file__).resolve().parent/'agent-tools'
 path=os.pathsep.join(p for p in os.environ.get('PATH','').split(os.pathsep) if pathlib.Path(p).resolve()!=own)
 real=shutil.which('gh',path=path)
 if not real:raise RuntimeError('GitHub CLI unavailable')
 env=dict(os.environ,PATH=path)
 if 'pr' in args and args[args.index('pr')+1:args.index('pr')+2]==['create']:
  from agent_pr_policy import provider
  who=provider(os.environ.get('BRAIN_PROVIDER','unknown'))
  repo=None
  for flag in ('-R','--repo'):
   if flag in args:repo=args[args.index(flag)+1]
  if not repo:repo=subprocess.check_output([real,'repo','view','--json','nameWithOwner','--jq','.nameWithOwner'],env=env,text=True).strip()
  labels=['agent:assisted','agent:author:'+who,'agent:pr-open']
  for label in labels:
   # No --force: never rewrite an existing label's metadata.
   probe=subprocess.run([real,'api',f'repos/{repo}/labels/'+label],env=env,capture_output=True)
   if probe.returncode:
    subprocess.run([real,'label','create',label,'-R',repo,'--color','7057ff','--description','Automated agent involvement: '+label],env=env,check=True,capture_output=True)
   args+=['--label',label]
 return subprocess.run([real,*args],input=data,text=True,env=env).returncode

if __name__=='__main__':sys.exit(main())
