#!/usr/bin/env python3
"""Offline rule publishing and score-link regression checks."""
import json
import runpy
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
slop = runpy.run_path(str(ROOT / "bin/slop-score"))


class RulesTest(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
