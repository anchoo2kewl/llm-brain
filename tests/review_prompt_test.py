#!/usr/bin/env python3
"""Pin the review prompt's evidence and approval rules."""
from pathlib import Path
import re
import unittest

ROOT=Path(__file__).resolve().parents[1]
PROMPT=(ROOT/'prompts/pr-review.md').read_text()


class ReviewPrompt(unittest.TestCase):
    def test_no_default_nothing_found_approval(self):
        self.assertNotIn('Approving. Nothing found.', PROMPT)
        self.assertIn('Post nothing', PROMPT)

    def test_scenarios_are_checked_privately_with_evidence_kind(self):
        for kind in ('`tested`', '`static`', '`unknown`'):
            self.assertIn(kind, PROMPT)
        self.assertIn('they are not posted', PROMPT)

    def test_public_output_stays_short_and_new(self):
        self.assertIn('no list of checks that passed', PROMPT)
        self.assertIn('no OWASP or scenario tables', PROMPT)
        self.assertIn('**Drop everything already said.**', PROMPT)
        self.assertIn('max ~100 words', PROMPT)

    def test_open_contract_or_unverified_risk_blocks_approval(self):
        event=next(l for l in PROMPT.splitlines() if l.startswith('Event:'))
        self.assertIn('Never APPROVE or REQUEST_CHANGES', event)

    def test_result_line_format_is_unchanged(self):
        self.assertTrue(re.search(r'^Last line of output exactly: `REVIEWED \{\{PR_URL\}\} <COMMENT\|SILENT> red=<n> '
                                  r'optional=<n> files=<x>/<y> slop=\{\{SLOP_VERDICT\}\}`$', PROMPT, re.M))


if __name__=='__main__':
    unittest.main()
