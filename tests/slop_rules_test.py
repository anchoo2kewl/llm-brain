#!/usr/bin/env python3
"""Offline rule publishing and score-link regression checks."""
import json
import runpy
import tempfile
import unittest
from types import SimpleNamespace
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
slop = runpy.run_path(str(ROOT / "bin/slop-score"))


class RulesTest(unittest.TestCase):
    def test_size_threshold_and_gradual_penalty(self):
        profile = json.loads((ROOT / "slop-profiles/default.json").read_text())
        params = profile["signals"]["Z01"]["params"]
        for config in (params, {}):
            for lines in (800, 2000, 3999, 4000):
                change = SimpleNamespace(changed_lines=lambda: lines)
                self.assertEqual(slop["d_pr_size"](change, {}, config), ([], None))
            for lines, severity in ((4001, 1 / 6000), (7000, .5), (10000, 1), (20000, 1)):
                change = SimpleNamespace(changed_lines=lambda: lines)
                hits, actual = slop["d_pr_size"](change, {}, config)
                self.assertTrue(hits)
                self.assertAlmostEqual(actual, severity)
        self.assertTrue(profile["signals"]["S05"].get("enabled", True))

    def setUp(self):
        self.profile = json.loads((ROOT / "slop-profiles/default.json").read_text())
        self.result = {
            "thresholds": self.profile["thresholds"], "verdict": "warn", "score": 15,
            "profile": {"id": "team-v1"}, "jev": "off", "docs_url": "https://example.com/scoring",
            "rules_url": "https://example.com/rules#old", "vetoes": [], "note": "",
            "signals": [{"id": "P03", "title": "How to verify", "category": "clarity",
                "evidence": [], "points": 8, "count": 1, "veto": False, "remedy": "Add verification steps."}],
            "assessed": {"files": 1, "review_lines": 5, "excluded": []}, "waived": [], "unassessed": []}

    def test_score_links_to_profile_and_specific_remedy(self):
        md = slop["render_md"](self.result, self.profile)
        self.assertIn("[How this is scored](https://example.com/scoring)", md)
        self.assertEqual(md.count("[P03](https://example.com/rules#rule-p03)"), 2)
        self.assertIn("rules and how to improve](https://example.com/rules#old)", md)

    def test_legacy_result_needs_no_rules_url(self):
        self.result.pop("rules_url")
        md = slop["render_md"](self.result, self.profile)
        self.assertIn("**P03**", md)
        self.assertNotIn("#rule-", md)

    def test_clean_and_skipped_scores_keep_guide_discoverable(self):
        self.result["signals"] = []
        self.assertIn("rules and how to improve", slop["render_md"](self.result, self.profile))
        self.result.update(verdict="skipped", note="release")
        self.assertIn("Profile rules and exemptions", slop["render_md"](self.result, self.profile))

    def test_generated_guide_has_every_enabled_rule_anchor_and_override_remedy(self):
        with tempfile.TemporaryDirectory() as tmp:
            overlay = Path(tmp) / "team.json"
            overlay.write_text(json.dumps({"signals": {"P03": {}}}))
            self.profile.update(extends=["default"], _files=[str(overlay)])
            self.profile["skip"]["base_branches"] = ["production"]
            md = slop["render_rules"](self.profile)
        for sid, rule in self.profile["signals"].items():
            if rule.get("enabled", True):
                self.assertIn(f'<a name="rule-{sid.lower()}"></a>', md)
        self.assertIn("Before requesting review", md)
        self.assertIn("PRs targeting `production`", md)
        self.assertIn("**To improve:** " + self.profile["signals"]["P03"]["remedy"], md)

    def test_inline_and_heading_test_evidence_is_accepted(self):
        params = self.profile["signals"]["P03"]["params"]
        for body in [
            '**Tests:** regression test fails on base and passes here.\n',
            '**Tests**: regression test fails on base and passes here.\n',
            '## Tests\nRegression test fails on base and passes here.\n',
            '## How to verify\n1. Open Issues; long names wrap and time stays visible.\n',
        ]:
            ch = SimpleNamespace(pr={}, body=body, review_lines=lambda: 100)
            with self.subTest(body=body):
                self.assertEqual(slop["d_verify_section"](ch, {}, params)[0], [])

    def test_other_sections_do_not_make_empty_tests_pass(self):
        params = self.profile["signals"]["P03"]["params"]
        for body in [
            '**Tests:**\n**Deploy order**\nThis API first, then the UI companion PR.\n',
            '## Tests\n## Deploy order\nThis API first, then the UI companion PR.\n',
            '## What\nThe endpoint validates allowed values before lookup.\n',
        ]:
            ch = SimpleNamespace(pr={}, body=body, review_lines=lambda: 100)
            with self.subTest(body=body):
                self.assertTrue(slop["d_verify_section"](ch, {}, params)[0])

    def test_company_guidance_and_reference_prs_are_published(self):
        self.profile["pr_guidance"] = ['- Keep Why short; include Tests and unchecked work.']
        self.profile["pr_examples"] = [{"title": "Small UI fix", "url": "https://example.com/pr/773"}]
        md = slop["render_rules"](self.profile)
        self.assertIn(self.profile["pr_guidance"][0], md)
        self.assertIn('[Small UI fix](https://example.com/pr/773)', md)


if __name__ == "__main__":
    unittest.main()
