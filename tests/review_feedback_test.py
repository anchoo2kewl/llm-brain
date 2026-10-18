#!/usr/bin/env python3
"""Exercise the actual shell feedback formatter with offline GitHub fixtures."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[1]


class ReviewFeedback(unittest.TestCase):
    def test_only_our_marked_machine_output_is_removed(self):
        with tempfile.TemporaryDirectory() as d:
            home=Path(d); (home/'bin').mkdir()
            code=(ROOT/'bin/fleet').read_text().split('existing_feedback() {',1)[1].split('\n}\n',1)[0]
            reviews=[{'user':{'login':'owner'},'state':'COMMENT','submitted_at':'today','commit_id':'abcdef12',
                      'body':'Real security finding\n<!-- llm-brain:score:start -->\nOLD_MACHINE_SCORE\n<!-- llm-brain:score:end -->\nHuman follow-up retained'},
                     {'user':{'login':'reviewer'},'state':'COMMENT','submitted_at':'today','commit_id':'abcdef12',
                      'body':'Other reviewer\n### AI Slop Score: keep external evidence'},
                     {'user':{'login':'owner'},'state':'COMMENT','submitted_at':'today','commit_id':'abcdef12',
                      'body':'Legacy review\n### AI Slop Score: preserve unmarked legacy context'}]
            comments=[{'user':{'login':'owner'},'created_at':'today','body':'### AI Slop Score: 10\nMACHINE_COMMENT\n<!-- llm-brain:ai-slop-score -->'},
                      {'user':{'login':'other'},'created_at':'today','body':'### AI Slop Score: quoted external feedback\n<!-- llm-brain:ai-slop-score -->'},
                      {'user':{'login':'owner'},'created_at':'today','body':'Human comment quoting <!-- llm-brain:ai-slop-score -->'}]
            (home/'reviews.json').write_text(json.dumps(reviews));(home/'comments.json').write_text(json.dumps(comments))
            fake=home/'bin/gh';fake.write_text('''#!/usr/bin/env python3
import os,sys,pathlib
root=pathlib.Path(os.environ['FIXTURE'])
path=sys.argv[2]
if '/pulls/' in path and path.endswith('/reviews'):print((root/'reviews.json').read_text())
elif '/issues/' in path:print((root/'comments.json').read_text())
''');fake.chmod(0o755)
            # gh's inline-comments --jq formatter is tested by leaving that fixture empty.
            shell='set -euo pipefail\ncfg() { echo owner; }\nexisting_feedback() {'+code+'\n}\nexisting_feedback acme/repo 1\n'
            r=subprocess.run(['bash','-c',shell],env=dict(os.environ,PATH=str(home/'bin')+':'+os.environ['PATH'],FIXTURE=str(home)),capture_output=True,text=True,check=True)
            self.assertNotIn('OLD_MACHINE_SCORE',r.stdout);self.assertNotIn('MACHINE_COMMENT',r.stdout)
            for kept in ['Real security finding','Human follow-up retained','external evidence','unmarked legacy context','quoted external feedback','Human comment quoting']:
                self.assertIn(kept,r.stdout)


if __name__=='__main__':unittest.main()
