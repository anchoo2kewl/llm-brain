#!/usr/bin/env python3
"""Exercise real fleet scheduling with fake GitHub, git, scoring and workers."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
OWNER = 'review_owner() {' + (ROOT/'bin/fleet').read_text().split('review_owner() {',1)[1].split('\n}\n',1)[0] + '\n}\n'
FUNCTION = OWNER + 'cmd_review_prs() {' + (ROOT/'bin/fleet').read_text().split('cmd_review_prs() {',1)[1].split('\n}\n',1)[0] + '\n}\n'


class SlackReviewDispatch(unittest.TestCase):
    def run_case(self, mode):
        with tempfile.TemporaryDirectory() as tmp:
            state=Path(tmp)
            for directory in ['prs','logs','chat/review-requests','reviews']:
                (state/directory).mkdir(parents=True)
            key='review-web-910'
            (state/f'prs/{key}.reviewed').write_text('same-head\n')
            (state/f'logs/{key}.1.log').write_text('BUDGET_STOPPED cap\n' if mode=='budget' else 'REVIEWED https://github.com/acme/web/pull/910 APPROVE\n')
            (state/'chat/review-requests/acme__web__910.sha256').write_text('new-request\n')
            if mode=='seen': (state/f'prs/{key}.chat-reviewed').write_text('new-request\n')
            script=r'''
set -euo pipefail
review_metric() { [[ "$1" != begin ]] || cat >/dev/null; return 0; }
paused() { return 1; }
worker_running() { return 1; }
review_queue() { echo https://github.com/acme/web/pull/910; }
review_capacity_left() { echo 1; }
cfg() {
 case "$1" in
  .pr_review.enabled) echo true;;
  .intake.assignee|.owner.github) echo owner;;
  '.slop.backfill_per_tick // 3') echo 0;;
  .pr_review.provider) echo claude;;
  .limits.review_timeout) echo 10m;;
  *) if [[ "$*" == '-o=json .owner.github' ]]; then echo '"owner"'; elif [[ "$*" == '-o=json .' ]]; then echo '{}'; else echo ''; fi;;
 esac
}
gh() {
 case "$*" in
  *'--json author'*) echo naj;;
  *'--json headRefOid'*) echo same-head;;
  *'--json headRepository,'*) echo '{"headRepositoryOwner":{"login":"acme"},"headRepository":{"name":"web"},"headRefName":"feature","baseRefName":"main","number":910}';;
  *) return 1;;
 esac
}
git() { if [[ "$1" == clone ]]; then mkdir -p "${@: -1}"; fi; return 0; }
existing_feedback() { echo 'Existing GitHub feedback'; }
slop_for() { SLOP='score'; SLOP_VERDICT=pass; }
prompt_env() { :; }
notes_for() { echo 'Naj: check the tenant filter; owner: preserve the agreed rule.'; }
post_mode() { echo 'dry run'; }
render() { printf 'BASE=%s\nPRIOR=%s\nNOTES=%s\n' "$REVIEW_BASE" "$PRIOR" "$NOTES"; }
spawn() { cat "$BRAIN_STATE/reviews/review-web-910/prompt.md" >> "$BRAIN_STATE/launched"; }
log() { :; }
'''
            if mode=='own': script=script.replace("echo naj;;", "echo OWNER;;")
            script+=FUNCTION+'\ncmd_review_prs\ncmd_review_prs\n'
            result=subprocess.run(['bash','-c',script],env=dict(os.environ,BRAIN_STATE=tmp,BRAIN_DIR=str(ROOT),HOST_NAME='test'),capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            launched=state/'launched'
            return launched.read_text() if launched.exists() else ''

    def test_new_feedback_runs_once_and_receives_notes_with_empty_delta(self):
        output=self.run_case('new')
        self.assertEqual(output.count('BASE=same-head'),1)
        self.assertIn('SAME-HEAD SLACK FOLLOW-UP',output)
        self.assertIn('Naj: check the tenant filter',output)

    def test_direct_self_review_guard_is_case_insensitive(self):
        self.assertEqual(self.run_case('own'), '')

    def test_consumed_feedback_does_not_restart_review(self):
        self.assertEqual(self.run_case('seen'),'')

    def test_guard_stop_is_not_bypassed_by_feedback(self):
        self.assertEqual(self.run_case('budget'),'')

    def test_discovery_failure_returns_error_before_any_dispatch(self):
        script = """
set -euo pipefail
review_metric() { [[ "$1" != begin ]] || cat >/dev/null; return 0; }
paused() { return 1; }
cfg() { if [[ "$*" == '-o=json .owner.github' ]]; then echo '"owner"'; elif [[ "$*" == '-o=json .' ]]; then echo '{}'; else echo true; fi; }
log() { :; }
review_queue() { echo https://github.com/acme/web/pull/1; return 1; }
gh() { echo 'unexpected dispatch' >&2; exit 99; }
""" + FUNCTION + '\ncmd_review_prs\n'
        result = subprocess.run(['bash', '-c', script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn('unexpected dispatch', result.stderr)

    def test_direct_review_rejects_invalid_configured_owner_before_github(self):
        import json
        for invalid in [None, '', '@me', 'bad owner', '-owner', 'owner--name', 42, ['owner']]:
            script = r"""
set -euo pipefail
cfg() { printf '%s\n' "$IDENTITY"; }
log() { :; }
gh() { echo unexpected-github >&2; exit 99; }
review_metric() { return 0; }
""" + FUNCTION + '\nONLY_PR=https://github.com/acme/web/pull/1 cmd_review_prs\n'
            result = subprocess.run(['bash','-c',script], env=dict(os.environ, IDENTITY=json.dumps(invalid)), capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertNotIn('unexpected-github', result.stderr)


class SlackQueue(unittest.TestCase):
    def queue(self, kind):
        import json
        with tempfile.TemporaryDirectory() as tmp:
            state=Path(tmp); (state/'prs').mkdir(); (state/'chat/review-requests').mkdir(parents=True)
            owner='other' if kind=='outside' else 'acme'
            meta=state/'chat/review-requests/acme__web__910.json'
            meta.write_text(json.dumps({'url':f'https://github.com/{owner}/web/pull/910','token':'request'}))
            if kind=='seen': (state/'prs/review-web-910.chat-reviewed').write_text('request\n')
            functions=OWNER
            (state/'bin').mkdir()
            helper=state/'bin/review-discovery'
            helper.write_text("#!/bin/sh\ncat >/dev/null\necho '[]'\n")
            helper.chmod(0o755)
            if kind=='failure': helper.write_text('#!/bin/sh\nexit 1\n')
            for name in ['chat_review_queue','review_queue']:
                functions+=name+'() {'+(ROOT/'bin/fleet').read_text().split('\n'+name+'() {',1)[1].split('\n}\n',1)[0]+'\n}\n'
            script=r"""
set -euo pipefail
review_metric() { [[ "$1" != inventory ]] || cat >/dev/null; return 0; }
cfg() {
 case "$*" in
  '-o=json .owner.github') echo '"OwNeR"';;
  '-o=json .pr_review.owners') echo '["acme"]';;
  '.pr_review.owners[]') echo acme;;
  '.pr_review.repo_regex') echo '^web$';;
  '.intake.assignee'|'.owner.github') echo owner;;
  *) echo '{}';;
 esac
}
gh() {
 if [[ "$1" == search ]]; then echo '[]'; return; fi
 echo called >> "$BRAIN_STATE/calls"
 local draft=false state=OPEN author=contributor
 [[ "$CASE" == draft ]] && draft=true
 [[ "$CASE" == closed ]] && state=MERGED
 [[ "$CASE" == own ]] && author=OWNER
 if [[ "$CASE" == deleted ]]; then
  echo '{"url":"https://github.com/acme/web/pull/910","author":null,"isDraft":false,"state":"OPEN"}'
  return
 fi
 printf '{"url":"https://github.com/acme/web/pull/910","author":{"login":"%s"},"isDraft":%s,"state":"%s"}\n' "$author" "$draft" "$state"
}
"""
            result=subprocess.run(['bash','-c',script+functions+'\nreview_queue\n'],env=dict(os.environ,BRAIN_STATE=tmp,BRAIN_DIR=tmp,CASE=kind),capture_output=True,text=True)
            self.assertEqual(result.returncode,1 if kind=='failure' else 0,result.stderr)
            if kind=='own': self.assertTrue(Path(str(meta)+'.owned').exists())
            return result.stdout.strip(), (state/'calls').exists(), Path(str(meta)+'.closed').exists()

    def test_helper_failure_emits_no_partial_chat_queue(self):
        self.assertEqual(self.queue('failure'), ('', False, False))

    def test_deleted_author_does_not_break_queue(self):
        self.assertEqual(self.queue('deleted')[0], 'https://github.com/acme/web/pull/910')

    def test_slack_request_does_not_require_github_reviewer_request(self):
        self.assertEqual(self.queue('open')[0],'https://github.com/acme/web/pull/910')

    def test_out_of_scope_or_seen_requests_make_no_github_call(self):
        for mode in ['outside','seen']:
            self.assertEqual(self.queue(mode),('',False,False))

    def test_draft_own_and_closed_prs_are_not_dispatched(self):
        for mode in ['draft','own','closed']:
            result=self.queue(mode)
            self.assertEqual(result[0],'')
            if mode=='closed': self.assertTrue(result[2])


if __name__=='__main__':
    unittest.main()
