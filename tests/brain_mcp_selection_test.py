import importlib.machinery,importlib.util,json,os,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
loader=importlib.machinery.SourceFileLoader('brain_mcp_selection','bin/brain-mcp')
spec=importlib.util.spec_from_loader(loader.name,loader);m=importlib.util.module_from_spec(spec);loader.exec_module(m)
class SelectionTest(unittest.TestCase):
 def test_desktop_selection_and_explicit_override(self):
  with tempfile.TemporaryDirectory() as tmp,patch.object(m.Path,'home',return_value=Path(tmp)),patch.dict(os.environ,{'BRAIN_HUB_URL':''}):
   with patch.object(m.sys,'platform','darwin'):
    desktop=Path(tmp)/'Library/Application Support/app.llmbrain.hub/hubs.json';desktop.parent.mkdir(parents=True)
    legacy=Path(tmp)/'.config/llm-brain/connections-hub.url';legacy.parent.mkdir(parents=True);legacy.write_text('http://100.64.0.2:8787')
    desktop.write_text(json.dumps({'selected':'http://127.0.0.1:8787','hubs':[{'url':'http://127.0.0.1:8787'}]}))
    self.assertEqual(m.hub_url(),'http://127.0.0.1:8787')
    with patch.dict(os.environ,{'BRAIN_HUB_URL':'http://100.64.0.3:8787'}):self.assertEqual(m.hub_url(),'http://100.64.0.3:8787')
    desktop.write_text(json.dumps({'selected':'http://unknown.invalid','hubs':[]}))
    self.assertEqual(m.hub_url(),'http://100.64.0.2:8787')
    desktop.write_text('broken')
    self.assertEqual(m.hub_url(),'http://100.64.0.2:8787')
