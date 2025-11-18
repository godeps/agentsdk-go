#!/usr/bin/env bash
set -euo pipefail

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required" >&2
  exit 1
fi

BASE_URL=${BASE_URL:-http://localhost:8080}
PROMPT=${PROMPT:-"用一句话总结 agentsdk-go"}
SESSION=${SESSION:-"curl-demo-$(date +%s)"}

json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))'
}

prompt_json=$(printf '%s' "$PROMPT" | json_escape)
session_json=$(printf '%s' "$SESSION" | json_escape)
stream_session_json=$(printf '%s' "${SESSION}-stream" | json_escape)

pretty() {
  if command -v jq >/dev/null 2>&1; then
    jq .
  else
    cat
  fi
}

run_payload=$(cat <<JSON
{
  "prompt": $prompt_json,
  "session_id": $session_json,
  "metadata": {"example": "non-stream"},
  "sandbox": {
    "allowed_paths": ["outputs"],
    "resource": {"max_cpu_percent": 75, "max_memory_mb": 512}
  }
}
JSON
)

echo "\n>>> POST $BASE_URL/v1/run"
curl -sS -X POST "$BASE_URL/v1/run" \
  -H 'Content-Type: application/json' \
  -d "$run_payload" | pretty

stream_payload=$(cat <<JSON
{
  "prompt": $prompt_json,
  "session_id": $stream_session_json,
  "sandbox": {"network_allow": ["api.anthropic.com"]}
}
JSON
)

echo "\n>>> POST $BASE_URL/v1/run/stream"
curl --no-buffer -N -X POST "$BASE_URL/v1/run/stream" \
  -H 'Content-Type: application/json' \
  -d "$stream_payload"

tool_payload=$(cat <<JSON
{
  "name": "bash_execute",
  "params": {"command": "ls -1 examples"},
  "sandbox": {"allowed_paths": ["examples"]},
  "usage": {"cpu_percent": 10}
}
JSON
)

echo "\n>>> POST $BASE_URL/v1/tools/execute"
curl -sS -X POST "$BASE_URL/v1/tools/execute" \
  -H 'Content-Type: application/json' \
  -d "$tool_payload" | pretty
