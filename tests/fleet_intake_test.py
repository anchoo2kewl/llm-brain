#!/usr/bin/env python3
"""Offline tests for which issues the fleet takes (bin/fleet-intake)."""
import importlib.machinery
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / 'bin/fleet-intake'
loader = importlib.machinery.SourceFileLoader('fleet_intake', str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
module = importlib.util.module_from_spec(spec)
loader.exec_module(module)

INTAKE = {
    'assignee': 'sam-dev',
    'authors': ['mona-l', 'octo-b'],
    'bot_authors': [{'login': 'github-actions[bot]', 'require_labels': ['security-fix'], 'repo_owners': ['acme']}],
}
SKIP = ['agent:stop', 'agent:done', 'agent:working', 'agent:pr-open', 'agent:blocked']
# What `gh issue list --json author` returns for the GitHub Actions bot.
ACTIONS = {'login': 'app/github-actions', 'is_bot': True}


def issue(number, author, labels=(), assignees=('sam-dev',), created='2026-11-01T00:00:00Z'):
    return {'number': number, 'title': 't', 'author': author, 'createdAt': created,
            'labels': [{'name': name} for name in labels],
            'assignees': [{'login': login} for login in assignees]}


def take(issues, repo='acme/platform', intake=INTAKE):
    return module.accepted(issues, repo, intake, SKIP)


class BotIntake(unittest.TestCase):
    def test_labelled_bot_issue_in_allowed_org_assigned_to_me_is_taken(self):
        self.assertEqual(take([issue(1, ACTIONS, ['security-fix'])]), [1])

    def test_rest_and_bare_bot_logins_match_too(self):
        for author in ({'login': 'github-actions[bot]', 'is_bot': True}, {'login': 'github-actions', 'is_bot': True}):
            self.assertEqual(take([issue(1, author, ['security-fix'])]), [1], author)

    def test_bot_issue_without_the_label_is_rejected(self):
        self.assertEqual(take([issue(1, ACTIONS, ['nightly-scan'])]), [])

    def test_bot_issue_outside_the_allowed_org_is_rejected(self):
        self.assertEqual(take([issue(1, ACTIONS, ['security-fix'])], repo='someone-else/platform'), [])

    def test_bot_issue_not_assigned_to_me_is_rejected(self):
        self.assertEqual(take([issue(1, ACTIONS, ['security-fix'], assignees=())]), [])
        self.assertEqual(take([issue(2, ACTIONS, ['security-fix'], assignees=('mona-l',))]), [])

    def test_a_human_with_the_bot_name_is_not_a_bot(self):
        self.assertEqual(take([issue(1, {'login': 'github-actions', 'is_bot': False}, ['security-fix'])]), [])

    def test_other_bots_and_unconfigured_bots_are_rejected(self):
        self.assertEqual(take([issue(1, {'login': 'app/dependabot', 'is_bot': True}, ['security-fix'])]), [])
        self.assertEqual(take([issue(2, ACTIONS, ['security-fix'])], intake={**INTAKE, 'bot_authors': []}), [])

    def test_a_rule_with_empty_labels_or_owners_matches_nothing(self):
        for rule in ({'login': 'github-actions[bot]', 'repo_owners': ['acme']},
                     {'login': 'github-actions[bot]', 'require_labels': ['security-fix']}):
            self.assertEqual(take([issue(1, ACTIONS, ['security-fix'])], intake={**INTAKE, 'bot_authors': [rule]}), [])

    def test_skip_labels_still_apply_to_bot_issues(self):
        self.assertEqual(take([issue(1, ACTIONS, ['security-fix', 'agent:working'])]), [])


class HumanIntake(unittest.TestCase):
    def test_allowed_author_needs_no_label(self):
        self.assertEqual(take([issue(1, {'login': 'mona-l', 'is_bot': False})]), [1])

    def test_unknown_author_is_rejected_even_with_the_bot_label(self):
        self.assertEqual(take([issue(1, {'login': 'stranger', 'is_bot': False}, ['security-fix'])]), [])

    def test_skip_labels_and_oldest_first(self):
        issues = [issue(3, {'login': 'octo-b'}, created='2026-11-03T00:00:00Z'),
                  issue(2, {'login': 'mona-l'}, ['agent:stop'], created='2026-11-02T00:00:00Z'),
                  issue(1, {'login': 'mona-l'}, created='2026-11-01T00:00:00Z')]
        self.assertEqual(take(issues), [1, 3])

    def test_author_match_is_exact_as_before(self):
        self.assertEqual(take([issue(1, {'login': 'Mona-L'})]), [])


class CommandLine(unittest.TestCase):
    def test_prints_repo_and_number(self):
        out = subprocess.run([sys.executable, str(SCRIPT), '--repo', 'acme/platform', '--intake', json.dumps(INTAKE),
                              '--skip', json.dumps(SKIP)],
                             input=json.dumps([issue(7, ACTIONS, ['security-fix']), issue(8, ACTIONS)]),
                             capture_output=True, text=True, check=True)
        self.assertEqual(out.stdout, 'acme/platform 7\n')

    def test_empty_input_means_no_candidates(self):
        out = subprocess.run([sys.executable, str(SCRIPT), '--repo', 'acme/platform', '--intake', json.dumps(INTAKE),
                              '--skip', '[]'], input='', capture_output=True, text=True, check=True)
        self.assertEqual(out.stdout, '')


if __name__ == '__main__':
    unittest.main()
