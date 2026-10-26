import importlib.machinery
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader('discovery', str(ROOT/'bin/review-discovery'))
spec = importlib.util.spec_from_loader(loader.name, loader)
d = importlib.util.module_from_spec(spec)
loader.exec_module(d)


class Discovery(unittest.TestCase):
    def test_paginates_beyond_fifty_and_one_hundred(self):
        calls = []
        def api(endpoint, **params):
            calls.append(params)
            page = params['page']
            return {'total_count': 251, 'items': list(range((page-1)*100, min(page*100, 251)))}
        with patch.object(d, 'api', api):
            self.assertEqual(len(d.search('is:pr', 0, 10)), 251)
        self.assertEqual([c['page'] for c in calls], [1, 2, 3])

    def test_partitions_search_cap_without_overlap(self):
        def api(endpoint, **params):
            if '00:00:00Z..1970-01-01T00:00:03Z' in params['q']:
                return {'total_count': 1001, 'items': []}
            return {'total_count': 1, 'items': [params['q']]}
        with patch.object(d, 'api', api):
            rows = d.search('is:pr', 0, 3)
        self.assertEqual(len(rows), 2)
        self.assertIn('00:00:00Z..1970-01-01T00:00:01Z', rows[0])
        self.assertIn('00:00:02Z..1970-01-01T00:00:03Z', rows[1])

    def test_incomplete_or_changing_results_fail_closed(self):
        for response in [{'incomplete_results': True}, {'total_count': 1, 'items': []}]:
            with patch.object(d, 'api', return_value=response), self.assertRaises(RuntimeError):
                d.search('is:pr', 0, 1)

    def test_all_signals_configured_identity_old_rows_and_issue_exclusion(self):
        queries = []
        row = {'html_url': 'https://github.com/acme/widget/pull/1',
               'repository_url': 'https://api.github.com/repos/acme/widget',
               'user': {'login': 'contributor'}, 'pull_request': {}, 'draft': False}
        def search(q):
            queries.append(q)
            return [row, {k:v for k,v in row.items() if k != 'pull_request'}]
        config = {'owner': {'github': 'configured-owner'}, 'pr_review': {
            'owners': ['acme'], 'teams': ['acme/maintainers'], 'discover_teams': True,
            'updated_since': '2099-01-01'}}
        with patch.object(d, 'api', return_value={'login': 'CONFIGURED-OWNER'}), patch.object(d, 'pages', return_value=[{'organization': {'login': 'acme'}, 'slug': 'maintainers'}]), patch.object(d, 'search', search):
            self.assertEqual(len(d.discover(config)), 1)
        self.assertEqual(len(queries), 4)
        for signal in ['assignee:configured-owner', 'review-requested:configured-owner', 'mentions:configured-owner', 'team-review-requested:acme/maintainers']:
            self.assertTrue(any(signal in q for q in queries))
        self.assertTrue(all('updated:' not in q and '@me' not in q for q in queries))

    def test_team_permission_failure_is_not_silently_ignored(self):
        config = {'owner': {'github': 'owner'}, 'pr_review': {'owners': ['acme']}}
        with patch.object(d, 'api', return_value={'login': 'different'}), self.assertRaises(ValueError):
            d.discover(config)

    def test_search_api_is_paced(self):
        with patch.object(d, 'LAST_SEARCH', 100), patch.object(d.time, 'monotonic', return_value=101), patch.object(d.time, 'sleep') as sleep, patch.object(d.subprocess, 'check_output', return_value='{}'):
            d.api('search/issues', q='is:pr')
        self.assertAlmostEqual(sleep.call_args.args[0], 1.1)

    def test_team_membership_paginates(self):
        def api(endpoint, **params):
            return [params['page']] * (100 if params['page'] == 1 else 1)
        with patch.object(d, 'api', api):
            self.assertEqual(len(list(d.pages('user/teams'))), 101)


if __name__ == '__main__':
    unittest.main()
