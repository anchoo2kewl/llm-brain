import json,pathlib,sys,unittest
from unittest.mock import patch
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[1]/'bin'))
import brain_gh as guard,agent_pr_policy as policy
class AgentPolicyTest(unittest.TestCase):
 def test_review_write_paths_cannot_approve_or_request_changes(self):
  cases=[(['pr','review','1','--approve'],''),(['pr','review','1','-ar'],''),(['pr','review','1','--request-changes'],''),(['api','repos/o/r/pulls/1/reviews','--input','-'],json.dumps({'event':'APPROVE'})),(['api','repos/o/r/pulls/1/reviews','-f','event=REQUEST_CHANGES'],''),(['api','repos/o/r/pulls/1/reviews/2/events','--input','-'],json.dumps({'event':'REQUEST_CHANGES'})),(['api','graphql','-f','query=mutation { addPullRequestReview(input:{event:APPROVE}) {clientMutationId}}'],''),(['api','graphql','--input','-'],json.dumps({'query':'mutation {submitPullRequestReview(input:{event:$event}){clientMutationId}}','variables':{'event':'APPROVE'}}))]
  for args,data in cases:
   with self.subTest(args=args):
    with self.assertRaises(ValueError):guard.validate(args,data)
 def test_advisory_and_read_only_paths_stay_available(self):
  for args,data in [(['pr','review','1','--comment','--body','Concrete finding'],''),(['api','repos/o/r/pulls/1/reviews'],''),(['api','repos/o/r/pulls/1/reviews','--input','-'],json.dumps({'event':'COMMENT','body':'Concrete finding'})),(['api','repos/o/r/pulls/1/reviews/2','-X','PUT','--input','-'],json.dumps({'body':'Updated finding'}))]:guard.validate(args,data)
 def test_review_is_not_code_authorship(self):
  self.assertEqual(policy.labels_for('openai','reviewer'),['agent:assisted','agent:reviewer:openai'])
  self.assertIsNone(policy.evidence({'body':'Reviewer said Claude found a bug','headRefName':'fix/human'},[]))
  self.assertEqual(policy.evidence({'body':'Generated with Claude Code'},[]),'claude')
  self.assertEqual(policy.evidence({},['Change\n\nAssisted-by: Codex (GPT-6)']),'codex')
 def test_additive_labels_preserve_human_labels(self):
  calls=[]
  def gh(*args,**kw):
   calls.append((args,kw))
   return [[{'name':'bug'},{'name':'agent:assisted'}]] if '--slurp' in args else {}
  with patch.object(policy,'gh',side_effect=gh):policy.ensure('org/repo',1,'claude','author','agent:pr-open')
  self.assertEqual(calls[-1][1]['payload'],{'labels':['agent:assisted','agent:author:claude','agent:pr-open']})
  self.assertFalse(any('DELETE' in a for a,k in calls))
