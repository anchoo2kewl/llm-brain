#!/usr/bin/env python3
"""Offline Slack identity, reference, routing and idempotence checks."""
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('chat_watch', str(ROOT/'bin/chat-watch'))
spec = importlib.util.spec_from_loader(loader.name, loader)
chat = importlib.util.module_from_spec(spec)
loader.exec_module(chat)


class RoutingTest(unittest.TestCase):
    def setUp(self):
        self.repos = ['acme/web', 'acme/api', 'acme/ui-sdk']
        self.state = {}
        self.calls = []

    def message(self, text, uid='NAJ', id=1, **extra):
        return dict(id=id, user_id=uid, text=text, conv_id='CHANNEL', conv_name='handoff',
                    conv_kind='channel', ts=str(id), at='2026-10-18T00:00:00Z', **extra)

    def prepare(self, rows):
        def lookup(repo, number):
            self.calls.append((repo, number))
            return 'task' if number == 17497 else 'review'
        return chat.prepare_messages(rows, {'user_id': 'OWNER', 'owner_id': 'OWNER'},
            {'NAJ': 'naj'}, {'naj': 'Naj'}, {'handoff': 'acme/web'}, self.repos,
            self.state, ['Anshuman Biswas', 'anchoo', 'anchoo2kewl'], 'Anshuman Biswas', lookup)

    def test_owner_instructions_are_not_skipped(self):
        row = self.prepare([self.message('Review #910; keep the existing product decision.', uid='OWNER')])[0]
        self.assertTrue(row['owner'])
        self.assertTrue(row['trusted'])
        self.assertTrue(row['review_requested'])
        self.assertEqual(row['who'], 'Anshuman Biswas')
        self.assertEqual(row['targets'], [{'ref':'acme/web#910','kind':'review'}])

    def test_alias_mentions_never_grant_sender_authority(self):
        for alias in ['Anshuman Biswas', 'ANChoo', '@anchoo2kewl']:
            row = self.prepare([self.message(alias + ' please review #910', uid='OTHER')])[0]
            self.assertTrue(row['to_me'])
            self.assertFalse(row['trusted'])
            self.assertFalse(row['review_requested'])
        self.assertEqual(self.calls, [])
        self.assertFalse(chat.addressed_to_owner('anchoo2kewl-fake account', ['anchoo']))

    def test_confirmed_issue_stays_task_context(self):
        row = self.prepare([self.message('Please review https://github.com/acme/web/issues/17497')])[0]
        self.assertEqual(row['targets'][0]['kind'], 'task')

    def test_pull_request_via_issues_url_is_still_review(self):
        row = self.prepare([self.message('Please review https://github.com/acme/web/issues/910')])[0]
        self.assertEqual(row['targets'][0]['kind'], 'review')

    def test_status_is_not_a_review_request(self):
        for text in ['#910 is approved, CI green, waiting for a codeowner merge.',
                     'S129 idle, awaiting a lane. #910 has 0 unaddressed threads.',
                     'Still pending the ai-slop re-runs on #910.']:
            self.assertFalse(self.prepare([self.message(text)])[0]['review_requested'])

    def test_slash_separated_refs_and_sdk_are_resolved(self):
        row = self.prepare([self.message('Please merge #910/#917/#918 after SDK #99.')])[0]
        self.assertEqual(set(row['refs']), {'acme/web#910','acme/web#917','acme/web#918','acme/ui-sdk#99'})

    def test_small_pr_ranges_are_expanded(self):
        row = self.prepare([self.message('Please re-run the scores for #906-#909.')])[0]
        self.assertEqual(set(row['refs']), {f'acme/web#{n}' for n in range(906,910)})

    def test_thread_reply_inherits_context_without_cross_channel_collision(self):
        self.prepare([self.message('Please review api#910', id=1)])
        reply = self.prepare([self.message('Please check the tenant filter.', id=2, thread_ts='1')])[0]
        self.assertEqual(reply['refs'], ['acme/api#910'])
        unrelated = self.prepare([self.message('Please review #910', id=3)])[0]
        self.assertEqual(unrelated['refs'], ['acme/web#910'])

    def test_no_ref_does_not_invent_a_task(self):
        row = self.prepare([self.message('Please keep the product decision.')])[0]
        self.assertEqual(row['targets'], [])

    def test_ambiguous_short_repo_is_not_guessed(self):
        self.assertIsNone(chat.resolve('api', ['acme/api','other/api']))

    def test_note_provenance_replay_dedupe_and_request_token(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            with patch.object(chat, 'NOTES', root/'notes'), patch.object(chat, 'CHAT', root/'chat'):
                chat.NOTES.mkdir(); chat.CHAT.mkdir()
                rows = self.prepare([self.message('Please review #910')])
                chat.save_messages(rows, self.state)
                note = chat.note_path('acme/web',910)
                first = note.read_text()
                token = (chat.CHAT/'review-requests/acme__web__910.sha256').read_text()
                chat.save_messages(rows, self.state)
                self.assertEqual(note.read_text(), first)
                # Same request text at another timestamp does not request another pass.
                chat.save_messages(self.prepare([self.message('Please review #910',id=2)]),self.state)
                self.assertEqual((chat.CHAT/'review-requests/acme__web__910.sha256').read_text(), token)
                self.assertIn('sender NAJ', first)
                self.assertIn('route review', first)
                self.assertEqual(self.state['routes']['1']['state'], 'routed')
                chat.save_messages(self.prepare([self.message('Please keep the product decision.',id=3)]),self.state)
                self.assertEqual(self.state['routes']['3']['state'], 'needs_reference')

    def test_unknown_github_target_does_not_schedule_review(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); (root/'notes').mkdir(); (root/'chat').mkdir()
            with patch.object(chat,'NOTES',root/'notes'), patch.object(chat,'CHAT',root/'chat'):
                with patch.object(chat,'target_kind',return_value='unknown'):
                    rows=self.prepare([self.message('Please review #910')])
                rows[0]['targets'][0]['kind']='unknown'
                chat.save_messages(rows,self.state)
                self.assertEqual(list((root/'chat/review-requests').iterdir()),[])

    def test_replay_upgrades_legacy_note_without_duplicate_body(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); (root/'notes').mkdir(); (root/'chat').mkdir()
            with patch.object(chat,'NOTES',root/'notes'), patch.object(chat,'CHAT',root/'chat'):
                row=self.prepare([self.message('Please review #910')])[0]
                path=chat.note_path('acme/web',910)
                path.write_text(f"### {row['at']} — {row['who']} in {row['where']}\n{row['text']}\n\n")
                chat.save_messages([row],self.state)
                self.assertEqual(path.read_text().count(row['text']),1)
                self.assertIn('llm-brain:slack-message:',path.read_text())

    def test_replay_preserves_live_cursor_and_does_not_call_insights(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp); (root/'chat').mkdir()
            (root/'chat/state.json').write_text(json.dumps({'after':77}))
            def cfg(key):
                return {'.chat.enabled':True,'.chat.trusted':{'NAJ':'naj'},'.people':{'naj':'Naj'},
                        '.chat.channel_repos':{'handoff':'acme/web'},'.owner.name':'Anshuman Biswas',
                        '.owner.github':'anchoo2kewl','.owner.aliases':[],'.chat.insights':True}.get(key)
            def hub(path):
                return {'user_id':'OWNER'} if path=='/api/chat/me' else [self.message('Still awaiting a decision.')]
            with patch.object(chat,'CHAT',root/'chat'), patch.object(chat,'NOTES',root/'notes'), patch.object(chat,'LOGS',root/'logs'), patch.object(chat,'cfg',side_effect=cfg), patch.object(chat,'known_repos',return_value=self.repos), patch.object(chat,'hub_get',side_effect=hub), patch.object(chat,'insights') as insights, patch.object(chat.sys,'argv',['chat-watch','--replay-channel','CHANNEL']):
                chat.main()
                insights.assert_not_called()
            self.assertEqual(json.loads((root/'chat/state.json').read_text())['after'],77)

    def test_type_lookup_checks_pull_request_field(self):
        from types import SimpleNamespace
        for body, code, want in [(dict(number=910,pull_request={}),0,'review'),
                                 (dict(number=17497),0,'task'),({},1,'unknown')]:
            with patch.object(chat.subprocess,'run',return_value=SimpleNamespace(returncode=code,stdout=json.dumps(body))):
                self.assertEqual(chat.target_kind('acme/web',body.get('number',1)),want)


if __name__ == '__main__':
    unittest.main()
