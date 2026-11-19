import importlib.machinery,importlib.util,json,tempfile,unittest
from pathlib import Path
from unittest.mock import patch,Mock
loader=importlib.machinery.SourceFileLoader('hub_update','bin/brain-hub-update');spec=importlib.util.spec_from_loader(loader.name,loader);m=importlib.util.module_from_spec(spec);loader.exec_module(m)
class HubUpdateTest(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name);(self.root/'bin').mkdir();(self.root/'bin/hub').write_bytes(b'previous-hub')
  self.u=m.Updater(self.root,self.root/'state');self.u.current=Mock(return_value='0.1.0')
 def test_default_auto_and_disabled_auto_does_not_check(self):
  self.assertTrue(self.u.status()['auto_enabled']);self.u.config['auto_enabled']=False
  with patch.object(m,'latest_release') as latest:self.u.check(automatic=True);latest.assert_not_called()
  m.atomic_json(self.u.state/'hub-update-request.json',{})
  with patch.object(m,'latest_release',return_value=None) as latest:self.u.check(automatic=True);latest.assert_called_once()
 def test_only_hub_releases_are_selected(self):
  url='https://github.com/'+m.REPO+'/releases/download/hub-v0.2.0/hub-update.json'
  releases=[{'tag_name':'client-v99.0.0'},{'tag_name':'hub-v0.3.0','draft':True},{'tag_name':'hub-v0.2.0','assets':[{'name':'hub-update.json','browser_download_url':url}]}]
  def fetch(url,limit):return json.dumps({'schema':1,'version':'0.2.0'} if url.endswith('.json') else releases).encode()
  with patch.object(m,'fetch',side_effect=fetch):self.assertEqual(m.latest_release()[2],'hub-v0.2.0')
 def test_checksum_and_asset_origin_rejected(self):
  asset={'name':'hub','browser_download_url':'https://github.com/'+m.REPO+'/releases/download/hub-v0.2.0/hub'}
  with patch.object(m,'fetch',return_value=b'corrupt'):
   with self.assertRaisesRegex(ValueError,'checksum'):m.checked_asset({'name':'hub','sha256':'a'*64},{'hub':asset},'hub-v0.2.0')
  with self.assertRaises(ValueError):m.asset_url({'browser_download_url':'https://example.com/hub'},'hub-v0.2.0')
 def test_busy_hub_is_not_restarted(self):
  self.u.health=Mock(return_value={'ready':False,'reason':'Migration active'});self.u.service=Mock()
  manifest={'version':'0.2.0','platforms':{m.target_key():{'name':'hub'}}}
  with patch.object(m,'checked_asset',return_value=b'new-hub'),patch.object(m,'run',return_value='0.2.0'):
   self.assertEqual(self.u.install(manifest,{},'hub-v0.2.0')['state'],'deferred')
  self.u.service.assert_not_called();self.assertEqual(self.u.binary.read_bytes(),b'previous-hub')
 def test_failed_health_restores_previous_executable(self):
  self.u.health=Mock(return_value={'ready':True});self.u.service=Mock();self.u.healthy_version=Mock(side_effect=[False,True])
  manifest={'version':'0.2.0','platforms':{m.target_key():{'name':'hub'}}}
  with patch.object(m,'checked_asset',return_value=b'new-hub'),patch.object(m,'run',return_value='0.2.0'):
   with self.assertRaisesRegex(RuntimeError,'health check'):self.u.install(manifest,{},'hub-v0.2.0')
  self.assertEqual(self.u.binary.read_bytes(),b'previous-hub');self.assertEqual(m.read_json(self.u.status_file,{})['state'],'rolled_back')
 def test_successful_install_checks_running_version(self):
  self.u.health=Mock(return_value={'ready':True});self.u.service=Mock();self.u.healthy_version=Mock(return_value=True)
  manifest={'version':'0.2.0','platforms':{m.target_key():{'name':'hub'}}}
  with patch.object(m,'checked_asset',return_value=b'new-hub'),patch.object(m,'run',return_value='0.2.0'):
   self.assertEqual(self.u.install(manifest,{},'hub-v0.2.0')['state'],'up_to_date')
  self.u.healthy_version.assert_called_once_with('0.2.0');self.assertEqual(self.u.binary.read_bytes(),b'new-hub')
 def test_no_downgrade(self):
  self.u.install=Mock()
  with patch.object(m,'latest_release',return_value=({'version':'0.0.9'},{},'hub-v0.0.9')):self.u.check(install=True)
  self.u.install.assert_not_called()
if __name__=='__main__':unittest.main()
