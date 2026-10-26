"""Review accounting and alerts use fake GitHub/Telegram, never a model call."""
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('monitor', str(ROOT/'bin/review-monitor'))
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
URL = 'https://github.com/acme/widget/pull/1'
CONFIG = {'owner': {'github': 'owner'}, 'pr_review': {'enabled': True, 'monitoring': {
    'expected': True, 'heartbeat_seconds': 100, 'missing_seconds': 100, 'stale_worker_seconds': 100}}}
LIVE = {'state': 'OPEN', 'headRefOid': 'head-a', 'author': {'login': 'contributor'}, 'isDraft': False}


class Monitor(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.sent = []
        self.send = lambda config, incident, active, counts: self.sent.append((incident, active))
        scheduler = patch.object(m, 'scheduler_present', return_value=True)
        scheduler.start()
        self.addCleanup(scheduler.stop)
        self.live = patch.object(m, 'current', return_value=LIVE.copy())
        self.live.start()
        self.addCleanup(self.live.stop)
        m.event(self.root, 'begin', [], CONFIG, now=1000)
        m.event(self.root, 'inventory', ['2'], [{'url': URL}], now=1000)
        m.event(self.root, 'head', [URL, 'head-a'], now=1000)

    def data(self):
        return json.loads((self.root/'review-health.json').read_text())

    def watch(self, now=1001, config=CONFIG):
        return m.watch(self.root, config, now=now, send=self.send)

    def test_discovery_failure_is_durable_deduplicated_and_recovers(self):
        m.event(self.root, 'discovery-failed', [], now=1000)
        self.watch()
        self.watch()
        self.assertEqual(self.sent, [('discovery_failed', True)])
        m.event(self.root, 'inventory', ['2'], [{'url': URL}], now=1002)
        self.watch(1003)
        self.watch(1004)
        self.assertEqual(self.sent[-1], ('discovery_failed', False))
        self.assertEqual(len(self.sent), 2)

    def test_missing_scheduler_and_missing_eligible_item(self):
        counts = self.watch(1101)
        self.assertEqual(counts['missing'], 1)
        self.assertIn(('scheduler_stale', True), self.sent)
        self.assertIn(('eligible_missing', True), self.sent)
        m.event(self.root, 'begin', [], CONFIG, now=1102)
        m.event(self.root, 'wait', ['capacity'], now=1102)
        self.watch(1103)
        self.assertIn(('scheduler_stale', False), self.sent)
        self.assertIn(('eligible_missing', False), self.sent)

    def test_scheduler_absence_alerts_even_with_fresh_heartbeat(self):
        with patch.object(m, 'scheduler_present', return_value=False):
            self.watch()
            self.watch()
        self.assertEqual(self.sent, [('scheduler_absent', True)])
        self.watch()
        self.assertEqual(self.sent[-1], ('scheduler_absent', False))

    def test_worker_cli_keeps_command_arguments_and_no_marker_is_not_success(self):
        result = subprocess.run([str(ROOT/'bin/review-monitor'), 'run', URL, 'head-a', 'owner', str(self.root/'empty.log'), '--', '/usr/bin/true'],
                                env=dict(os.environ, BRAIN_STATE=str(self.root)), capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.data()['items'][URL]['status'], 'failed')
        self.assertEqual(self.data()['summary']['completed'], 0)

    def test_expected_capacity_and_quota_waits_are_quiet(self):
        m.event(self.root, 'begin', [], CONFIG, now=1101)
        m.event(self.root, 'wait', ['capacity'], now=1101)
        self.assertEqual(self.watch(1102)['queued'], 1)
        self.assertEqual(self.sent, [])
        m.event(self.root, 'budget', [URL], now=1102)
        self.assertEqual(self.watch(1103)['blocked'], 1)
        self.assertEqual(self.sent, [])

    def test_intentional_stop_does_not_send_outage_or_false_recovery(self):
        for config in [dict(CONFIG, paused=True), dict(CONFIG, pr_review={'enabled': False}),
                       dict(CONFIG, pr_review={'enabled': True, 'monitoring': {'expected': False}})]:
            self.watch(2000, config)
        self.assertEqual(self.sent, [])
        with m.ledger(self.root) as data:
            data['alerts']['scheduler_stale'] = True
        self.watch(2001, dict(CONFIG, paused=True))
        self.assertEqual(self.sent, [])

    def test_stuck_lost_worker_and_no_progress_recovery(self):
        log = self.root/'worker.log'
        log.touch()
        os.utime(log, (1000, 1000))
        with m.ledger(self.root) as data:
            data['items'][URL].update(status='running', pid=123, log=str(log), started=1000)
        m.event(self.root, 'begin', [], CONFIG, now=1101)
        with patch.object(m, 'live_worker', return_value=True):
            self.assertEqual(self.watch(1102)['blocked'], 1)
            self.assertIn(('worker_stuck', True), self.sent)
            os.utime(log, (1103, 1103))
            self.assertEqual(self.watch(1104)['running'], 1)
            self.assertIn(('worker_stuck', False), self.sent)
        with patch.object(m, 'live_worker', return_value=False):
            self.assertEqual(self.watch(1105)['failed'], 1)
        self.assertIn(('review_failed', True), self.sent)

    def test_alert_delivery_failure_is_not_acknowledged(self):
        m.event(self.root, 'discovery-failed', [], now=1000)
        with self.assertRaises(RuntimeError):
            m.watch(self.root, CONFIG, now=1001, send=lambda *a: (_ for _ in ()).throw(RuntimeError('offline')))
        self.assertEqual(self.data()['alerts'], {})
        self.watch()
        self.assertEqual(self.sent, [('discovery_failed', True)])

    def test_completion_requires_success_marker_and_actual_review(self):
        log = self.root/'run.log'
        log.write_text(f'REVIEWED {URL} APPROVE red=0\n')
        with patch.object(m.subprocess, 'call', return_value=0), patch.object(m, 'verify', return_value=42):
            self.assertEqual(m.run_worker(self.root, [URL, 'head-a', 'OWNER', str(log), 'fake']), 0)
        counts = self.watch()
        self.assertEqual(counts['reviewed'], 1)
        self.assertEqual(counts['completed'], 1)
        self.watch()
        self.assertEqual(self.watch()['completed'], 1)
        with patch.object(m, 'current', return_value=dict(LIVE, headRefOid='head-b')):
            self.assertEqual(self.watch()['completed'], 0)
        self.assertEqual(self.data()['items'][URL]['head'], 'head-b')

    def test_fake_completion_and_budget_are_not_reviews(self):
        log = self.root/'run.log'
        for text, code, receipt in [(f'REVIEWED {URL} APPROVE red=0\n', 0, None),
                                     ('', 0, 42), (f'REVIEWED {URL} APPROVE red=0\n', 1, 42),
                                     ('BUDGET_STOPPED reserve\n', 0, 42)]:
            log.write_text(text)
            with patch.object(m.subprocess, 'call', return_value=code), patch.object(m, 'verify', return_value=receipt):
                m.run_worker(self.root, [URL, 'head-a', 'owner', str(log), 'fake'])
            self.assertEqual(m.summary(self.data(), 1001)['reviewed'], 0)

    def test_successful_same_head_review_is_not_erased_by_failed_feedback_retry(self):
        log = self.root/'run.log'
        log.write_text(f'REVIEWED {URL} COMMENT red=0\n')
        with patch.object(m.subprocess, 'call', return_value=0), patch.object(m, 'verify', return_value=42):
            m.run_worker(self.root, [URL, 'head-a', 'owner', str(log), 'fake'])
        log.write_text('worker error\n')
        with patch.object(m.subprocess, 'call', return_value=1):
            m.run_worker(self.root, [URL, 'head-a', 'owner', str(log), 'fake'])
        counts = m.summary(self.data(), 1001)
        self.assertEqual(counts['completed'], 1)
        self.assertEqual(counts['failed'], 1)
        self.assertEqual(counts['completed_unique_heads'], 1)

    def test_retry_at_same_head_counts_once(self):
        log = self.root/'run.log'
        log.write_text(f'REVIEWED {URL} COMMENT red=1\n')
        for _ in range(3):
            with patch.object(m.subprocess, 'call', return_value=0), patch.object(m, 'verify', return_value=42):
                m.run_worker(self.root, [URL, 'head-a', 'owner', str(log), 'fake'])
        self.assertEqual(m.summary(self.data(), 1001)['completed'], 1)
        self.assertEqual(len(self.data()['items']), 1)
        self.assertEqual(m.summary(self.data(), 1001)['completed_unique_heads'], 1)

    def test_legacy_launch_marker_alone_is_not_a_completed_review(self):
        (self.root/'prs').mkdir()
        (self.root/'logs').mkdir()
        (self.root/'prs/review-widget-1.reviewed').write_text('head-a\n')
        self.assertEqual(self.watch()['completed'], 0)
        (self.root/'logs/review-widget-1.1.log').write_text(f'REVIEWED {URL} APPROVE red=0\n')
        with patch.object(m, 'verify', return_value=42):
            self.assertEqual(self.watch()['completed'], 1)

    def test_github_verification_matches_owner_head_submission_and_time(self):
        row = {'id': 42, 'user': {'login': 'OWNER'}, 'commit_id': 'head-a', 'state': 'APPROVED',
               'submitted_at': '1970-01-01T00:16:41Z'}
        with patch.object(m, 'gh', return_value=[[row]]):
            self.assertEqual(m.verify(URL, 'head-a', 'owner', 1000), 42)
            self.assertIsNone(m.verify(URL, 'head-b', 'owner', 1000))
            self.assertIsNone(m.verify(URL, 'head-a', 'other', 1000))
            self.assertIsNone(m.verify(URL, 'head-a', 'owner', 1002))
        with patch.object(m, 'gh', return_value=[[dict(row, state='PENDING')]]):
            self.assertIsNone(m.verify(URL, 'head-a', 'owner', 1000))

    def test_worker_completion_during_watch_is_not_overwritten(self):
        def live(url):
            with m.ledger(self.root) as data:
                data['items'][URL].update(status='completed', finished=1001,
                    receipt={'head': 'head-a', 'review_id': 42}, head_verified=True)
            return LIVE.copy()
        with patch.object(m, 'current', side_effect=live):
            self.assertEqual(self.watch(1101)['completed'], 1)
        self.assertNotIn(('eligible_missing', True), self.sent)

    def test_current_head_api_failure_never_counts_stale_completion(self):
        with m.ledger(self.root) as data:
            data['items'][URL].update(status='completed', receipt={'head': 'head-a', 'review_id': 42}, head_verified=True)
        with patch.object(m, 'current', side_effect=subprocess.CalledProcessError(1, 'gh')):
            counts = self.watch()
        self.assertEqual(counts['completed'], 0)
        self.assertEqual(counts['blocked'], 1)
        self.assertIn(('verification_failed', True), self.sent)
        self.assertEqual(self.watch()['completed'], 1)


if __name__ == '__main__':
    unittest.main()
