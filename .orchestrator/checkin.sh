#!/bin/bash
# One Jules check-in: waits up to <secs> for a reply, then prints liveness from
# every source (REST state, Jules CLI, open PRs, newest activities + latest
# agent message). Run in the background; see HANDOVER §5 for the cadence.
# usage: .orchestrator/checkin.sh <session> <secs> <issue>
cd "$(dirname "$0")/.." && source ~/.config/jules/env
JULES_CLI=${JULES_CLI:-$(ls ~/.npm/_npx/*/node_modules/.bin/jules 2>/dev/null | head -1)}
python3 .orchestrator/jules.py wait-reply "$1" "$2" >/dev/null 2>&1
date -u +"now %H:%M:%S"
[ -n "$JULES_CLI" ] && timeout 60 "$JULES_CLI" remote list --session 2>/dev/null | grep "^ *$1 " | sed "s/  */ /g; s/^/cli: /"
python3 .orchestrator/jules.py get "$1" | python3 -c 'import json,sys; d=json.load(sys.stdin); print("state", d["state"], d["updateTime"], "outputs-prs", d.get("pullRequests"))'
gh pr list -R gargi-indic/gargi-flow --search "#$3 in:title" --json number,title,createdAt --jq '.[] | "open PR #\(.number) \(.createdAt) \(.title)"'
python3 .orchestrator/jules.py activities "$1" 60 | python3 -c '
import json,sys,re
acts=json.load(sys.stdin)
msgs=[a for a in acts if a.get("agentMessaged")]
acts=sorted(acts,key=lambda a:a["createTime"])[-4:]+msgs[-1:]
for a in acts:
  keys=[k for k in a if k not in ("name","createTime","originator","id")]
  files=[]
  for x in a.get("artifacts",[]):
    p=(x.get("changeSet") or {}).get("gitPatch",{}).get("unidiffPatch","")
    if p: files=sorted(set(re.findall(r"^\+\+\+ b/(\S+)",p,re.M)))
  print(a["createTime"], keys, files or "", str(a.get("agentMessaged") or "")[:1500])'
