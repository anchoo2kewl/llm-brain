#!/usr/bin/env python3
"""Refresh the fixed, non-secret policy files embedded in a hub service build."""
import argparse,shutil
from pathlib import Path
FILES=['bin/cheap-review','bin/api-review-queue','bin/agent_pr_policy.py','bin/brain-pr-policy','bin/brain_gh.py','bin/agent-tools/gh','bin/llm-run','bin/fleet','prompts/pr-review.md','prompts/implement.md','prompts/babysit.md']
def stage(root):
 for name in FILES:
  target=root/'hub/policyassets'/name;target.parent.mkdir(parents=True,exist_ok=True)
  shutil.copy2(root/name,target)
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('source',type=Path);stage(p.parse_args().source.resolve())
