import importlib.machinery
import importlib.util
import json
import os
import subprocess
import unittest
from unittest.mock import patch

loader=importlib.machinery.SourceFileLoader('brain_mcp','bin/brain-mcp')
spec=importlib.util.spec_from_loader(loader.name,loader);m=importlib.util.module_from_spec(spec);loader.exec_module(m)
class DiagnosticsTest(unittest.TestCase):
 def test_native_report_reads_metadata_without_dump_memory(self):
  import tempfile
  event={'debug_meta':{'images':[{'code_file':'/Applications/Brain Hub.app/Contents/MacOS/BrainHub'}]},'level':'fatal'}
  with tempfile.TemporaryDirectory() as directory:
   path=m.Path(directory)/'test.ghosttycrash'
   path.write_bytes(b'{}\n{}\n'+json.dumps(event).encode()+b'\n'+b'PRIVATE-DUMP-MEMORY')
   self.assertEqual(m.native_crash_event(path),event)
   with patch.object(m,'diagnostic_reports',return_value={'3/test.ghosttycrash':path}):
    output=m.read_diagnostic('3/test.ghosttycrash')
    self.assertIn('fatal',output['text'])
    self.assertNotIn('PRIVATE-DUMP-MEMORY',output['text'])
   event['debug_meta']['images'][0]['code_file']='/Applications/Other.app/Contents/MacOS/Other'
   path.write_bytes(b'{}\n{}\n'+json.dumps(event).encode()+b'\n')
   self.assertIsNone(m.native_crash_event(path))

 def test_unknown_report_and_traversal_are_rejected(self):
  with patch.object(m,'diagnostic_reports',return_value={}):
   for report in ('../../etc/passwd','1/unknown.ips'):
    with self.assertRaises(ValueError):m.read_diagnostic(report)
 def test_local_reports_are_bounded_and_redacted(self):
  import tempfile
  with tempfile.TemporaryDirectory() as directory:
   path=m.Path(directory)/'app.log'
   path.write_text('token=secret-value\n'+('x'*1048576))
   with patch.object(m,'diagnostic_reports',return_value={'0/app.log':path}):
    result=m.read_diagnostic('0/app.log')
    self.assertTrue(result['truncated'])
    self.assertNotIn('secret-value',result['text'])
    self.assertIn('[REDACTED]',result['text'])

class BrainMCPTest(unittest.TestCase):
 def invoke(self,name,args):return m.respond({'method':'tools/call','params':{'name':name,'arguments':args}})
 def test_stdio_and_control_desk_instructions(self):
  p=subprocess.run(['bin/brain-mcp'],input='\n'.join(json.dumps(x) for x in [{'jsonrpc':'2.0','id':1,'method':'initialize'},{'jsonrpc':'2.0','method':'notifications/initialized'},{'jsonrpc':'2.0','id':2,'method':'tools/list'}])+'\n',capture_output=True,text=True,check=True)
  rows=[json.loads(x) for x in p.stdout.splitlines()];self.assertEqual(len(rows),2);self.assertIn('Terminal ids belong to one hub',rows[0]['result']['instructions']);self.assertIn('desktop_update',[t['name'] for t in rows[1]['result']['tools']]);self.assertIn('session_wait',[t['name'] for t in rows[1]['result']['tools']])
 def test_reject_arbitrary_routes_extra_parameters_and_invalid_priority(self):
  with patch.object(m,'request') as r:
   for name,args in [('queue_control',{'repo':'acme/app','number':1,'action':'reserve'}),('queue_issue',{'repo':'acme/app','number':1,'priority':101}),('session_send',{'id':1,'text':'unverified input'}),('session_send',{'id':1,'text':'unverified','expected_screen_hash':''}),('session_create',{'prompt':'job','url':'https://elsewhere.invalid'})]:
    self.assertTrue(self.invoke(name,args)['isError'])
   r.assert_not_called()
 def test_screen_hash_and_send_guard_forwarding(self):
  with patch.object(m,'request',return_value={'screen':'terminal text'}) as r:
   x=json.loads(self.invoke('session_screen',{'id':12})['content'][0]['text']);self.assertEqual(len(x['screen_hash']),64)
  with patch.object(m,'request',return_value={'ok':True}) as r:
   self.invoke('session_send',{'id':12,'expected_screen_hash':x['screen_hash'],'text':'authorized guidance'});self.assertEqual(r.call_args.args[1]['expected_screen_hash'],x['screen_hash'])
 def test_queue_and_session_creation_use_fixed_central_endpoints(self):
  with patch.object(m,'request',return_value={}) as r:
   self.invoke('queue_issue',{'repo':'acme/app','number':12,'after':[{'repo':'acme/app','number':10}]});self.assertEqual(r.call_args.args[0],'/api/intake/queue')
   self.invoke('session_create',{'prompt':'Implement approved job','provider':'codex','worktree_slug':'job-12'});self.assertEqual(r.call_args.args[0],'/terminals/new');self.assertEqual(r.call_args.args[1]['cli'],'codex');self.assertTrue(r.call_args.kwargs['form'])
 def test_malformed_parameters_do_not_crash_server(self):
  with self.assertRaises(ValueError):m.respond({'method':'tools/call','params':[]})
  self.assertTrue(self.invoke('queue_list',[]) ['isError'])
 def test_credentials_in_hub_url_rejected(self):
  for url in ('https://user:password@example.invalid','file:///tmp/file','https://example.invalid/path','https://example.invalid?token=secret'):
   with patch.dict(os.environ,{'BRAIN_HUB_URL':url}):
    with self.assertRaises(ValueError):m.hub_url()
class SessionWaitTest(unittest.TestCase):
 def test_wait_calls_the_hub_once_with_a_long_timeout(self):
  with patch.object(m,'request',return_value={'state':'quiet'}) as r:
   out=m.respond({'method':'tools/call','params':{'name':'session_wait','arguments':{'id':36,'text':'DONE','timeout_seconds':120}}})
   self.assertFalse(out.get('isError'))
   path=r.call_args[0][0]
   self.assertTrue(path.startswith('/api/terminals/36/wait?') and 'until=output' in path and 'text=DONE' in path)
   self.assertEqual(r.call_args.kwargs['timeout'],150)
 def test_wait_many_waits_for_all_by_default(self):
  with patch.object(m,'request',return_value={'done':True}) as r:
   self.assertFalse(m.respond({'method':'tools/call','params':{'name':'session_wait_many','arguments':{'ids':[3,4]}}}).get('isError'))
   path=r.call_args[0][0];self.assertIn('ids=3%2C4',path);self.assertIn('mode=all',path)
class ControlTest(unittest.TestCase):
 def call(self,name,args):return m.respond({'method':'tools/call','params':{'name':name,'arguments':args}})
 def test_move_clone_close_and_workspace_use_fixed_endpoints(self):
  with patch.object(m,'request',return_value={'job':'a'*64}) as r:
   self.assertFalse(self.call('session_move',{'id':5,'to':'Studio'}).get('isError'));self.assertEqual(r.call_args.args[:2],('/api/terminals/5/migrate',{'to':'Studio','close_source':True}))
   self.call('session_move',{'id':5,'to':'Studio','mode':'clone'});self.assertEqual(r.call_args.args[1],{'to':'Studio','clone':True})
   self.call('move_status',{'job':'a'*64});self.assertEqual(r.call_args.args[0],'/api/migrations/'+'a'*64)
   self.call('session_close',{'id':5});self.assertEqual(r.call_args.args[0],'/api/terminals/5/close')
   self.call('session_rename',{'id':5,'title':'x'});self.assertTrue(r.call_args.kwargs['form'])
   self.call('workspace_control',{'action':'layout','layout':'4'});self.assertEqual(r.call_args.args[:2],('/api/workspace/intent',{'action':'layout','layout':'4'}))
   n=r.call_count
   for name,args in [('move_status',{'job':'../../api/terminals'}),('session_move',{'id':5,'to':'x','mode':'teleport'}),('workspace_control',{'action':'focus'}),('workspace_control',{'action':'exec','terminal':5}),('session_close',{'id':0})]:
    self.assertTrue(self.call(name,args)['isError'])
   self.assertEqual(r.call_count,n)
  tools={t['name']:t for t in m.TOOLS};self.assertTrue(tools['session_close']['annotations']['destructiveHint'])
 def test_move_folder_options_and_mapping_management(self):
  with patch.object(m,'request',return_value={'mappings':[]}) as r:
   self.assertFalse(self.call('session_move',{'id':5,'to':'Studio','destination_path':'~/different/repo','pin_path':True}).get('isError'))
   self.assertEqual(r.call_args.args[1],{'to':'Studio','close_source':True,'destination_path':'~/different/repo','pin_path':True})
   self.call('session_move',{'id':5,'to':'Studio','without_folder':True});self.assertTrue(r.call_args.args[1]['without_folder'])
   self.call('move_folders',{'action':'list'});self.assertEqual(r.call_args.args[0],'/api/move-folders')
   pin={'action':'set','from_host':'laptop','source_path':'~/repo','to':'Studio','destination_path':'~/other/repo'}
   self.assertFalse(self.call('move_folders',pin).get('isError'));self.assertEqual(r.call_args.args,('/api/move-folders',pin))
   self.call('move_folders',{k:v for k,v in pin.items() if k!='destination_path'}|{'action':'remove'});self.assertEqual(r.call_args.args[1]['action'],'remove')
   n=r.call_count
   for args in ({'id':5,'to':'Studio','pin_path':True},{'id':5,'to':'Studio','pin_path':'yes'},{'id':5,'to':'Studio','without_folder':True,'destination_path':'~/repo'}):self.assertTrue(self.call('session_move',args)['isError'])
   self.assertEqual(r.call_count,n)
 def test_hub_update_is_separate_and_validated(self):
  with patch.object(m,'request',return_value={'auto_enabled':True}) as r:
   self.assertFalse(self.call('hub_update',{'action':'status'}).get('isError'));self.assertEqual(r.call_args.args[0],'/api/hub-update')
   for args in ({'action':'check'},{'action':'install'},{'action':'settings','auto_enabled':False},{'action':'settings','interval_hours':12}):
    self.assertFalse(self.call('hub_update',args).get('isError'));self.assertEqual(r.call_args.args,('/api/hub-update',args))
   n=r.call_count
   for args in ({'action':'settings'},{'action':'settings','interval_hours':0},{'action':'settings','auto_enabled':'yes'},{'action':'install','auto_enabled':True},{'action':'shell'}):self.assertTrue(self.call('hub_update',args)['isError'])
   self.assertEqual(r.call_count,n)
 def test_desktop_update_is_local_and_validated(self):
  with patch.object(m,'desktop_update',return_value={'version':'0.1.4'}) as update:
   self.assertFalse(self.call('desktop_update',{'action':'status'}).get('isError'))
   update.assert_called_once_with('status')
   for args in ({'action':'install','hub':'studio'},{'action':'run'},{'action':'install','url':'https://example.org/other.zip'}):
    self.assertTrue(self.call('desktop_update',args)['isError'])
   self.assertEqual(update.call_count,1)
 def test_hub_is_resolved_only_through_listed_peers(self):
  nodes={'self':{'node':'mac'},'peers':[{'ok':True,'url':'https://studio.example.ts.net','info':{'node':'studio'}},{'ok':False,'url':'http://10.0.0.9:8787','info':{'node':'off'}}]}
  def fake(path,payload=None,form=False,timeout=40,base=None):
   return nodes if path=='/api/cluster/nodes' else {'base':base}
  with patch.dict(os.environ,{'BRAIN_HUB_URL':'http://127.0.0.1:8787'}),patch.object(m,'request',side_effect=fake):
   out=json.loads(self.call('session_detail',{'id':3,'hub':'studio'})['content'][0]['text']);self.assertEqual(out['base'],'https://studio.example.ts.net')
   out=json.loads(self.call('session_detail',{'id':3,'hub':'mac'})['content'][0]['text']);self.assertEqual(out['base'],'http://127.0.0.1:8787')
   for hub in ('off','https://evil.invalid','nope'):self.assertTrue(self.call('session_detail',{'id':3,'hub':hub})['isError'])
class ListTest(unittest.TestCase):
 def test_session_list_spans_online_peers_unless_hub_named(self):
  nodes={'self':{'node':'mac'},'peers':[{'ok':True,'url':'https://studio.example.ts.net','info':{'node':'studio'}},{'ok':False,'url':'http://10.0.0.9:8787','info':{'node':'off'}}]}
  def fake(path,payload=None,form=False,timeout=40,base=None):
   if path=='/api/cluster/nodes':return nodes
   return [{'id':7}] if base=='https://studio.example.ts.net' else []
  with patch.dict(os.environ,{'BRAIN_HUB_URL':'http://127.0.0.1:8787'}),patch.object(m,'request',side_effect=fake):
   rows=json.loads(m.respond({'method':'tools/call','params':{'name':'session_list','arguments':{}}})['content'][0]['text'])
   self.assertEqual(rows,[{'id':7,'hub':'studio'}])
   rows=json.loads(m.respond({'method':'tools/call','params':{'name':'session_list','arguments':{'hub':'mac'}}})['content'][0]['text'])
   self.assertEqual(rows,[])
if __name__=='__main__':unittest.main()
