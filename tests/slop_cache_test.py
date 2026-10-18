#!/usr/bin/env python3
"""Offline Jev cache tests; the judge is a temporary executable, never a real API."""
import concurrent.futures
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('slop_score', str(ROOT/'bin/slop-score'))
spec = importlib.util.spec_from_loader(loader.name, loader)
slop = importlib.util.module_from_spec(spec)
loader.exec_module(slop)


class JevCache(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name); self.cache = self.home/'cache'; self.counter = self.home/'requests'
        (self.home/'bin').mkdir()
        fake = self.home/'bin/jev'
        fake.write_text('''#!/usr/bin/env python3
import json,os,pathlib,sys,time
f=pathlib.Path(sys.argv[2]);doc=json.loads(f.read_text())
with open(os.environ['FAKE_COUNT'],'a') as out:out.write('request\\n')
assert os.environ['JEV_RETRIES']=='0'
time.sleep(10 if os.environ.get("FAKE_SLOW") else .03)
if os.environ.get('FAKE_FAIL'):sys.exit(1)
if os.environ.get('FAKE_EMPTY'):sys.exit(0)
a={q:.9 for q in doc['questions']}
if os.environ.get('FAKE_PARTIAL'):a.pop(next(iter(a)))
if os.environ.get('FAKE_INVALID'):a[next(iter(a))]=1.5
f.with_name(f.stem+'.answers.json').write_text(json.dumps(a))
''')
        fake.chmod(0o755)
        original = slop.BRAIN; slop.BRAIN = self.home
        self.addCleanup(setattr, slop, 'BRAIN', original)
        env = patch.dict(os.environ, {'FAKE_COUNT':str(self.counter)})
        env.start(); self.addCleanup(env.stop)
        self.doc = {'title':'test','state':{'repo':'acme/api','code':'redacted evidence'},
                    'questions':{'a':{'question':'bug?','yes':'yes','no':'no'},'b':{'question':'scope?','yes':'yes','no':'no'}}}
        self.calls = 0

    def run_score(self, **kwargs):
        self.calls += 1
        return slop.run_jev([('pr',self.doc,{})],3,self.home/('out'+str(self.calls)),kwargs.pop('timeout',5),cache_dir=self.cache,**kwargs)

    def count(self):
        return len(self.counter.read_text().splitlines()) if self.counter.exists() else 0

    def test_repeats_stay_independent_and_second_run_has_zero_requests(self):
        a, f, stats = self.run_score()
        self.assertEqual(f,[]); self.assertEqual(a['a'],[.9,.9,.9]); self.assertEqual(stats,{'hits':0,'requests':3})
        a, f, stats = self.run_score()
        self.assertEqual(self.count(),3); self.assertEqual(stats,{'hits':3,'requests':0})
        self.assertEqual(len(list(self.cache.glob('*.json'))),3)

    def test_evidence_question_model_and_endpoint_changes_invalidate(self):
        self.run_score()
        self.doc['state']['code'] = 'changed'; self.run_score()
        self.doc['questions']['a']['question'] = 'changed?'; self.run_score()
        with patch.dict(os.environ, {'TYPESAFE_DEFAULT_MODEL':'other-model'}):self.run_score()
        with patch.dict(os.environ, {'TYPESAFE_BASE_URL':'https://other.invalid'}):self.run_score()
        self.assertEqual(self.count(),15)

    def test_expired_or_corrupt_cache_is_replaced(self):
        self.run_score()
        for f in self.cache.glob('*.json'):
            d=json.loads(f.read_text());d['at']=0;f.write_text(json.dumps(d))
        self.run_score(); self.assertEqual(self.count(),6)
        for f in self.cache.glob('*.json'):f.write_text('bad json')
        self.run_score(); self.assertEqual(self.count(),9)

    def test_partial_invalid_or_failed_answers_are_never_cached(self):
        for key in ('FAKE_PARTIAL','FAKE_INVALID','FAKE_FAIL'):
            with patch.dict(os.environ,{key:'1'}):
                _,failures,_=self.run_score();self.assertTrue(failures)
            self.assertFalse(list(self.cache.glob('*.json')))
        self.run_score();self.assertEqual(self.count(),12)

    def test_cached_probabilities_are_validated(self):
        self.run_score()
        for f in self.cache.glob('*.json'):
            d=json.loads(f.read_text());d['answers']['a']=True;f.write_text(json.dumps(d))
        self.run_score();self.assertEqual(self.count(),6)

    def test_cache_can_be_disabled(self):
        self.run_score();self.run_score(cache_ttl=0);self.assertEqual(self.count(),6)

    def test_concurrent_scorers_share_three_samples(self):
        with concurrent.futures.ThreadPoolExecutor(2) as ex:
            results = list(ex.map(lambda i:slop.run_jev([('pr',self.doc,{})],3,self.home/('parallel'+str(i)),5,cache_dir=self.cache),range(2)))
        self.assertEqual(self.count(),3)
        self.assertEqual(sum(r[2]['hits'] for r in results),3)

    def test_stale_answer_file_cannot_count_as_new_response(self):
        self.run_score()
        self.calls=0  # intentionally reuse the old output folder
        with patch.dict(os.environ,{'FAKE_EMPTY':'1'}):
            _,failures,_=self.run_score(cache_ttl=0)
            self.assertTrue(failures)

    def test_timed_out_processes_leave_no_cached_answers(self):
        with patch.dict(os.environ,{'FAKE_SLOW':'1'}):
            _,failures,_=self.run_score(timeout=.1)
        self.assertEqual(len(failures),3)
        self.assertFalse(list(self.cache.glob('*.json')))

    def test_no_candidate_packets_do_no_work(self):
        answers,failures,stats=slop.run_jev([],3,self.home/'empty',5,cache_dir=self.cache)
        self.assertEqual((answers,failures,stats),({},[],{'hits':0,'requests':0}))
        self.assertFalse(self.cache.exists())

    def test_majority_rule_is_unchanged(self):
        self.assertEqual(slop.judged([.95,.1,.1],3),0)
        self.assertGreater(slop.judged([.95,.9,.1],3),0)


if __name__ == '__main__':unittest.main()
