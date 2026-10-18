#!/usr/bin/env python3
"""Offline tests for deterministic fleet dispatch decisions."""
import importlib.machinery
import importlib.util
from pathlib import Path
import unittest

loader = importlib.machinery.SourceFileLoader('fleet_plan', str(Path(__file__).resolve().parents[1] / 'bin/fleet-plan'))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)


class BabysitPlan(unittest.TestCase):
    def setUp(self):
        self.pr = {'headRefOid': 'a', 'reviews': [], 'comments': [], 'reviewDecision': 'REVIEW_REQUIRED',
                   'mergeStateStatus': 'BLOCKED', 'statusCheckRollup': [
                       {'name': 'build', 'status': 'IN_PROGRESS', 'conclusion': ''}]}

    def plan(self, previous=None, **kwargs):
        return module.babysit(self.pr, previous, 'owner', kwargs.pop('notes', ''), **kwargs)

    def test_pending_checks_do_not_start_a_worker(self):
        self.assertFalse(self.plan()[0])

    def test_success_without_approval_still_waits(self):
        self.pr['statusCheckRollup'][0]['conclusion'] = 'SUCCESS'
        self.assertFalse(self.plan()[0])

    def test_new_feedback_interrupts_waiting_and_only_runs_once(self):
        previous = self.plan()[2]
        self.pr['comments'] = [{'author': {'login': 'reviewer'}, 'body': 'Fix the tenant boundary'}]
        run, _, current = self.plan(previous)
        self.assertTrue(run)
        self.assertFalse(self.plan(current)[0])
        self.pr['comments'][0]['body'] = 'Edited: fix the account boundary'
        self.assertTrue(self.plan(current)[0])

    def test_our_own_comment_and_slop_status_do_not_wake_worker(self):
        previous = self.plan()[2]
        self.pr['comments'] = [{'author': {'login': 'owner'}, 'body': 'AI Slop Score'}]
        self.pr['statusCheckRollup'].append({'context': 'ai-slop-score', 'state': 'SUCCESS'})
        self.assertFalse(self.plan(previous)[0])

    def test_ci_transition_without_feedback_waits(self):
        previous = self.plan()[2]
        self.pr['statusCheckRollup'][0]['conclusion'] = 'SUCCESS'
        self.assertFalse(self.plan(previous)[0])

    def test_failure_and_merge_conflict_are_actionable(self):
        self.pr['statusCheckRollup'][0]['conclusion'] = 'FAILURE'
        self.assertTrue(self.plan()[0])
        self.pr['statusCheckRollup'] = []
        self.pr['mergeStateStatus'] = 'DIRTY'
        self.assertTrue(self.plan()[0])

    def test_settled_approved_pr_can_merge(self):
        self.pr['reviewDecision'] = 'APPROVED'
        self.pr['statusCheckRollup'][0]['conclusion'] = 'SUCCESS'
        run, _, previous = self.plan()
        self.assertTrue(run)
        self.assertFalse(self.plan(previous)[0])

    def test_trusted_notes_are_actionable_once(self):
        run, _, previous = self.plan(notes='hash')
        self.assertTrue(run)
        self.assertFalse(self.plan(previous, notes='hash')[0])

    def test_missing_marker_needs_worker(self):
        self.assertTrue(self.plan(marked=False)[0])

    def test_optional_approval_config_is_respected(self):
        self.pr['statusCheckRollup'] = [{'context': 'ci', 'state': 'SUCCESS'}]
        self.assertTrue(self.plan(require_approval=False)[0])

    def test_order_of_checks_does_not_change_fingerprint(self):
        self.pr['statusCheckRollup'].append({'context': 'test', 'state': 'PENDING'})
        previous = self.plan()[2]
        self.pr['statusCheckRollup'].reverse()
        self.assertEqual(self.plan(previous)[1], 'unchanged')


if __name__ == '__main__':
    unittest.main()
