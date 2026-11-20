"""Agent attribution; code authorship and advisory review are separate roles."""
import json,re,subprocess

def gh(*args,payload=None):
 p=subprocess.run(['gh',*args],input=json.dumps(payload) if payload is not None else None,capture_output=True,text=True,timeout=45)
 if p.returncode:raise RuntimeError('agent_attribution_failed')
 return json.loads(p.stdout) if p.stdout.strip() else None

def provider(value):
 return value if value in ('claude','codex','gemini','openai') else 'unknown'

def labels_for(who,role):
 if role not in ('author','reviewer'):raise ValueError('invalid agent role')
 return ['agent:assisted','agent:'+role+':'+provider(who)]

def ensure(repo,number,who,role,state=None):
 if not re.fullmatch(r'[\w.-]+/[\w.-]+',repo) or not str(number).isdigit():raise ValueError('invalid PR')
 desired=labels_for(who,role)+([state] if state else [])
 if state and state not in ('agent:working','agent:pr-open','agent:reviewing','agent:reviewed'):raise ValueError('invalid agent state')
 applied=gh('api',f'repos/{repo}/issues/{number}/labels')
 if set(desired)<={x['name'] for x in applied}:return desired
 existing=gh('api',f'repos/{repo}/labels?per_page=100','--paginate','--slurp')
 names={x['name'] for page in existing for x in page}
 for name in desired:
  if name not in names:
   gh('api',f'repos/{repo}/labels','--input','-',payload={'name':name,'color':'7057ff','description':'Automated agent assistance: '+name.removeprefix('agent:')})
 gh('api',f'repos/{repo}/issues/{number}/labels','--input','-',payload={'labels':desired})
 return desired

def evidence(pr,commits):
 text=(pr.get('body') or '')+'\n'+(pr.get('headRefName') or '')+'\n'+'\n'.join(commits)
 if not re.search(r'llm-brain:issue=|generated (?:with|by).*?(?:claude|codex|gemini)|(?:^|\n)(?:co-authored-by|assisted-by):[^\n]*(?:claude|codex|openai|anthropic|gemini)|^(?:claude|codex|gemini)/',text,re.I|re.M):return None
 for name,pattern in [('claude',r'claude|anthropic'),('codex',r'codex'),('gemini',r'gemini'),('openai',r'openai')]:
  if re.search(pattern,text,re.I):return name
 return 'unknown'

def reconcile():
 rows=gh('search','prs','--author','@me','--state','open','--limit','1000','--json','url,repository,number')
 if len(rows)>=1000:raise RuntimeError('PR search limit reached; attribution audit incomplete')
 tagged=[]
 for row in rows:
  url=row['url'];pr=gh('pr','view',url,'--json','body,headRefName,commits')
  commits=[x.get('messageHeadline','')+'\n'+x.get('messageBody','') for x in pr.get('commits',[])]
  who=evidence(pr,commits)
  if who:
   ensure(row['repository']['nameWithOwner'],row['number'],who,'author','agent:pr-open');tagged.append(url)
 return tagged
