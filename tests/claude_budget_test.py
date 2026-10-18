#!/usr/bin/env python3
"""Offline budget enforcement, concurrency, compaction/resume and installer tests."""
import importlib.machinery
import importlib.util
import json
import multiprocessing
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

BRAIN = Path(__file__).resolve().parents[1]


def load(name):
    loader = importlib.machinery.SourceFileLoader(name.replace('-', '_'), str(BRAIN / 'bin' / name))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


budget = load('claude-budget')
installer = load('install-claude-limits')


def parallel_call(args):
    payload, policy, directory = args
    return budget.check(payload, policy, Path(directory))


class BudgetTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.root = self.home / 'projects' / 'root.jsonl'
        self.root.parent.mkdir()
        self.root.touch()
        self.state = self.home / 'state'
        self.policy = dict(budget.DEFAULTS)

    def payload(self, event='PreToolUse', tool='Read', tid='t1', transcript=None):
        return {'session_id': 'root', 'transcript_path': str(transcript or self.root),
                'hook_event_name': event, 'tool_name': tool, 'tool_use_id': tid}

    def check(self, payload=None):
        return budget.check(payload or self.payload(), self.policy, self.state)

    def append(self, mid='m1', output=100, inp=10, read=100, write=20, path=None, tools=None):
        path = path or self.root
        path.parent.mkdir(parents=True, exist_ok=True)
        row = {'type': 'assistant', 'message': {'id': mid, 'model': 'claude-opus-example',
               'usage': {'output_tokens': output, 'input_tokens': inp,
                         'cache_read_input_tokens': read, 'cache_creation_input_tokens': write},
               'content': tools or []}}
        with path.open('a') as f:
            f.write(json.dumps(row) + '\n')

    def test_six_agent_calls_then_denied(self):
        for i in range(6):
            self.assertEqual(self.check(self.payload(tool='Agent', tid=str(i))), {})
        denied = self.check(self.payload(tool='Agent', tid='extra'))
        self.assertEqual(denied['hookSpecificOutput']['permissionDecision'], 'deny')
        self.assertEqual(self.check(self.payload(tid='read')), {})

    def test_pending_agent_already_written_in_transcript_cannot_bypass_cap(self):
        for i in range(7):
            self.append(mid='m' + str(i), tools=[{'type': 'tool_use', 'name': 'Agent', 'id': str(i)}])
            result = self.check(self.payload(tool='Agent', tid=str(i)))
            if i < 6:
                self.assertEqual(result, {})
            else:
                self.assertEqual(result['hookSpecificOutput']['permissionDecision'], 'deny')

    def test_duplicate_hook_call_is_idempotent(self):
        self.policy['max_agent_calls'] = 1
        self.assertEqual(self.check(self.payload(tool='Task')), {})
        self.assertEqual(self.check(self.payload(tool='Task')), {})
        self.assertIn('hookSpecificOutput', self.check(self.payload(tool='Task', tid='t2')))

    def test_resume_consumes_an_agent_call_too(self):
        self.policy['max_agent_calls'] = 1
        self.check(self.payload(tool='Agent', tid='start'))
        p = self.payload(tool='Agent', tid='resume'); p['tool_input'] = {'resume': 'agent-a'}
        self.assertEqual(self.check(p)['hookSpecificOutput']['permissionDecision'], 'deny')

    def test_nested_agents_denied_from_both_hook_shapes(self):
        p = self.payload(tool='Agent'); p['agent_id'] = 'child'
        self.assertIn('hookSpecificOutput', self.check(p))
        child = self.root.with_suffix('') / 'subagents' / 'agent-child.jsonl'
        self.assertIn('hookSpecificOutput', self.check(self.payload(tool='Agent', transcript=child)))

    def test_concurrent_reservations_share_atomic_cap(self):
        args = [(self.payload(tool='Agent', tid=str(i)), self.policy, str(self.state)) for i in range(18)]
        with multiprocessing.get_context('fork').Pool(6) as pool:
            results = pool.map(parallel_call, args)
        self.assertEqual(sum(not r for r in results), 6)

    def test_streamed_blocks_count_once_using_maximum_usage(self):
        self.policy['max_output_tokens'] = 1000
        self.append(output=400); self.append(output=500)
        self.assertNotIn('continue', self.check())
        self.append(mid='m2', output=500)
        self.assertFalse(self.check()['continue'])

    def test_parent_and_child_usage_share_the_same_budget(self):
        self.policy['max_output_tokens'] = 1000
        self.append(output=500)
        child = self.root.with_suffix('') / 'subagents' / 'agent-child.jsonl'
        self.append(mid='m2', output=500, path=child)
        self.assertFalse(self.check(self.payload(transcript=child))['continue'])
        self.assertFalse(self.check()['continue'])

    def test_cache_reads_and_writes_count_toward_input_budget(self):
        self.policy['max_input_tokens'] = 1000
        self.append(inp=0, read=800, write=200)
        self.assertFalse(self.check()['continue'])

    def test_response_limit_counts_unique_message_ids(self):
        self.policy['max_responses'] = 2
        self.append(mid='a'); self.append(mid='a'); self.append(mid='b')
        self.assertFalse(self.check()['continue'])

    def test_tool_limit_stops_instead_of_deny_retry_loop(self):
        self.policy['max_tool_calls'] = 2
        self.assertEqual(self.check(self.payload(tid='a')), {})
        self.assertTrue(self.check(self.payload(tid='b')).get('continue', True))
        self.assertFalse(self.check(self.payload(tid='c'))['continue'])

    def test_compaction_and_resume_do_not_refund_spent_budget(self):
        self.policy['max_output_tokens'] = 1000
        self.append(mid='a', output=600)
        self.check()
        self.root.write_text('')  # compact/rewrite
        self.check(self.payload(event='UserPromptSubmit'))
        self.append(mid='b', output=400)
        self.assertFalse(self.check(self.payload(event='UserPromptSubmit'))['continue'])

    def test_post_tool_batch_stops_before_next_model_request(self):
        self.policy['max_output_tokens'] = 100
        self.append(output=100)
        self.assertFalse(self.check(self.payload(event='PostToolBatch'))['continue'])

    def test_warning_at_eighty_percent_only_once(self):
        self.policy['max_output_tokens'] = 1000
        self.append(output=800)
        self.assertIn('systemMessage', self.check())
        self.assertNotIn('systemMessage', self.check())

    def test_partial_json_line_is_retried(self):
        text = json.dumps({'type': 'assistant', 'message': {'id': 'a', 'usage': {'output_tokens': 100000}}})
        self.root.write_text(text[:30]); self.check()
        with self.root.open('a') as f:
            f.write(text[30:] + '\n')
        self.assertFalse(self.check()['continue'])

    def test_corrupt_state_fails_closed_at_cli_boundary(self):
        self.check()
        next(self.state.glob('*.json')).write_text('not json')
        policy = self.home / 'policy.json'; policy.write_text(json.dumps(self.policy))
        p = subprocess.run([str(BRAIN / 'bin/claude-budget'), '--policy', str(policy), '--state-dir', str(self.state)],
                           input=json.dumps(self.payload()), capture_output=True, text=True, check=True)
        self.assertFalse(json.loads(p.stdout)['continue'])

    def test_guard_stop_signals_the_headless_adapter(self):
        self.append(output=100000)
        policy = self.home / 'policy.json'; policy.write_text(json.dumps(self.policy))
        marker = self.home / 'budget-stop'
        p = subprocess.run([str(BRAIN / 'bin/claude-budget'), '--policy', str(policy), '--state-dir', str(self.state)],
                           input=json.dumps(self.payload()), capture_output=True, text=True, check=True,
                           env=dict(os.environ, BRAIN_CLAUDE_BUDGET_STOP_FILE=str(marker)))
        self.assertFalse(json.loads(p.stdout)['continue'])
        self.assertIn('output tokens', marker.read_text())

    def test_invalid_policy_fails_closed(self):
        policy = self.home / 'policy.json'; policy.write_text('{"max_agent_calls":0}')
        p = subprocess.run([str(BRAIN / 'bin/claude-budget'), '--policy', str(policy), '--state-dir', str(self.state)],
                           input=json.dumps(self.payload()), capture_output=True, text=True, check=True)
        self.assertFalse(json.loads(p.stdout)['continue'])

    def test_existing_historical_agent_calls_count(self):
        tools = [{'type': 'tool_use', 'name': 'Agent', 'id': str(i)} for i in range(6)]
        self.append(tools=tools)
        self.assertEqual(self.check(self.payload(tool='Agent', tid='new'))['hookSpecificOutput']['permissionDecision'], 'deny')

    def test_initial_prompt_can_run_before_transcript_exists(self):
        self.root.unlink()
        self.assertEqual(self.check(self.payload(event='UserPromptSubmit')), {})

    def test_disappeared_transcript_is_an_error_not_a_refund(self):
        self.check(); self.root.unlink()
        with self.assertRaises(ValueError):
            self.check()

    def test_installer_preserves_existing_settings_and_is_idempotent(self):
        path = self.home / '.claude/settings.json'; path.parent.mkdir()
        old = {'model': 'opus[1m]', 'statusLine': {'type': 'command', 'command': 'my-status'},
               'permissions': {'allow': ['Read']}, 'hooks': {'PreToolUse': [{'hooks': [
                   {'type': 'command', 'command': 'agentshield hook'}]}]}}
        path.write_text(json.dumps(old))
        for _ in range(2):
            installer.install(self.home, BRAIN / 'bin/claude-budget', Path('/usr/bin/python3'))
        new = json.loads(path.read_text())
        self.assertEqual(new['model'], 'opus')
        self.assertEqual(new['statusLine'], old['statusLine'])
        self.assertEqual(new['permissions'], old['permissions'])
        self.assertEqual(len(new['hooks']['PreToolUse']), 2)
        self.assertEqual(new['env']['CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS'], '2')
        self.assertEqual(new['env']['CLAUDE_CODE_DISABLE_1M_CONTEXT'], '1')
        self.assertEqual(new['env']['CLAUDE_CODE_AUTO_COMPACT_WINDOW'], '100000')
        self.assertEqual(new['env']['CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH'], '1')
        self.assertEqual(new['env']['CLAUDE_CODE_MAX_TURNS'], '80')
        for event in installer.EVENTS:
            self.assertEqual(new['hooks'][event][-1]['hooks'][0]['onFailure'], 'block')
        self.assertEqual(json.loads(path.with_name('settings.pre-claude-limits.json').read_text()), old)


if __name__ == '__main__':
    unittest.main()
