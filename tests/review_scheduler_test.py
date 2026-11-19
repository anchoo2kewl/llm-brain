"""Offline checks for the opt-in review-only LaunchAgent."""
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest

import slack_review_dispatch_test

ROOT = Path(__file__).resolve().parents[1]
PLIST = ROOT/'launchd/com.llm-brain.reviews.plist'


class ReviewScheduler(unittest.TestCase):
    def test_scheduler_calls_only_existing_review_entrypoint(self):
        config = plistlib.loads(PLIST.read_bytes())
        self.assertEqual(config['Label'], 'com.llm-brain.reviews')
        self.assertEqual(config['ProgramArguments'], ['__BRAIN__/bin/fleet', 'review-prs'])
        self.assertEqual(config['StartInterval'], 300)
        self.assertTrue(config['RunAtLoad'])
        self.assertNotIn('ONLY_PR', config['EnvironmentVariables'])
        self.assertNotIn('BRAIN_OVERLAY', config['EnvironmentVariables'])
        # Execute the real CLI using a local lib that stops at its pause gate.
        # Any issue, babysitter, discovery or worker activity is a failure.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'bin').mkdir()
            (root/'bin/fleet').write_bytes((ROOT/'bin/fleet').read_bytes())
            (root/'bin/review-monitor').write_bytes((ROOT/'bin/review-monitor').read_bytes())
            (root/'bin/review-monitor').chmod(0o755)
            (root/'bin/lib.sh').write_text('''
BRAIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cfg() { if [[ "$*" == '-o=json .owner.github' ]]; then echo '"owner"'; else echo '{"owner":{"github":"owner"}}'; fi; }
paused() { return 0; }
log() { echo "$*"; }
gh() { echo unexpected-github >&2; return 99; }
''')
            args = [a.replace('__BRAIN__', tmp) for a in config['ProgramArguments']]
            result = subprocess.run(['bash', *args], capture_output=True, text=True,
                                    env={**os.environ, 'BRAIN_STATE': tmp})
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), 'paused')
            self.assertEqual(result.stderr, '')

    def test_scheduled_reviews_preserve_dedup_and_budget_stop(self):
        # These run the same real cmd_review_prs selected by the plist command,
        # with fake external services. Neither case may launch a review worker.
        dispatch = slack_review_dispatch_test.SlackReviewDispatch()
        self.assertEqual(dispatch.run_case('seen'), '')
        self.assertEqual(dispatch.run_case('budget'), '')

    def test_watchdog_is_independent_and_opt_in(self):
        config = plistlib.loads((ROOT/'launchd/com.llm-brain.review-watch.plist').read_bytes())
        self.assertEqual(config['ProgramArguments'], ['__BRAIN__/bin/review-watch'])
        self.assertEqual(config['Label'], 'com.llm-brain.review-watch')
        self.assertNotIn('com.llm-brain.review-watch', (ROOT/'bootstrap.sh').read_text())
        self.assertNotIn('fleet', (ROOT/'bin/review-watch').read_text().split('source ', 1)[1])

    def test_bootstrap_does_not_automatically_load_review_scheduler(self):
        self.assertNotIn('com.llm-brain.reviews', (ROOT/'bootstrap.sh').read_text())


if __name__ == '__main__':
    unittest.main()
