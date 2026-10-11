#!/usr/bin/env bash
# Claude Code injection-effectiveness probe (macOS / Linux).
#
# trajector's routing rests on two behaviors of Claude Code that are not
# documented contracts and can shift between claude versions:
#
#   1. A project's .claude/settings.local.json `env` block overrides the
#      shell environment for ANTHROPIC_BASE_URL. (If this flips, an
#      injected project would silently bypass the proxy whenever the
#      user exports their own base URL.)
#   2. SessionStart and UserPromptSubmit hooks actually run, which is
#      what keeps the proxy alive across sessions.
#
# This script probes both against the claude binary on PATH. Run it
# after a Claude Code upgrade. It never touches your real configuration:
# everything happens in a throwaway project directory. The model
# requests go to two local listeners that answer a non-retryable error,
# so no model request leaves the machine and nothing is billed. Claude
# Code's own background requests do not follow ANTHROPIC_BASE_URL and
# still go to their usual hosts.
#
# Not part of CI: it needs a locally installed claude.
#
# Usage: scripts/claude_injection_probe.sh
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
need() { command -v "$1" >/dev/null 2>&1 || { echo "SKIP: $1 not found on PATH" >&2; exit 2; }; }
need claude
need go

workdir="$(mktemp -d)"
pids=()
cleanup() {
  for pid in "${pids[@]:-}"; do kill "$pid" 2>/dev/null || true; done
  rm -rf "$workdir"
}
trap cleanup EXIT
# An interrupt stops the probe, also when claude handles the signal and
# exits normally, so a stopped run never reports a result.
trap 'exit 130' INT
trap 'exit 143' TERM

# The listener runs as a built binary, not through go run: cleanup must
# kill the process that listens, and go run would leave it as an
# orphaned child.
listener="$workdir/probelistener"
(cd "$repo_root" && go build -o "$listener" ./scripts/probelistener)

# start_listener <name> -> sets listener_addr, appends its pid; requests
# land in $workdir/<name>.log as "METHOD path"
start_listener() {
  local name="$1" out="$workdir/$1.out"
  : > "$workdir/$name.log"
  "$listener" -log "$workdir/$name.log" > "$out" &
  pids+=($!)
  for _ in $(seq 1 100); do
    if grep -q LISTENING "$out" 2>/dev/null; then
      listener_addr="$(awk '/LISTENING/{print $2; exit}' "$out")"
      return
    fi
    sleep 0.2
  done
  echo "FAIL: listener $name did not start" >&2
  exit 1
}

start_listener settings   # the port the settings env block points at
settings_addr="$listener_addr"
start_listener shell      # the port the shell environment points at
shell_addr="$listener_addr"

project="$workdir/project"
mkdir -p "$project/.claude"
marker_session="$workdir/hook-session-start"
marker_prompt="$workdir/hook-user-prompt"
cat > "$project/.claude/settings.local.json" <<EOF
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://${settings_addr}"
  },
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "touch ${marker_session}"}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "touch ${marker_prompt}"}]}
    ]
  }
}
EOF

echo "settings env block -> http://${settings_addr}"
echo "shell environment  -> http://${shell_addr}"
echo "running claude -p in $project ..."

# The API key forces the key-authenticated path so the base URL is used;
# the value is a fake that no server accepts. claude exits nonzero on
# the probe's 400 answer — expected.
(
  cd "$project"
  ANTHROPIC_BASE_URL="http://${shell_addr}" \
  ANTHROPIC_API_KEY="probe-key-never-valid" \
  claude -p "Reply with the single word: ok" >/dev/null 2>&1 || true
)

# Only a model request proves which base URL won. Claude Code warms up
# its connection with HEAD /api/hello, without credentials or a body;
# that request alone does not mean the shell environment won.
model_request="POST /v1/messages"
warmup_request="HEAD /api/hello"
settings_models="$(grep -cxF "$model_request" "$workdir/settings.log" || true)"
shell_warmups="$(grep -cxF "$warmup_request" "$workdir/shell.log" || true)"
shell_others="$(grep -vxF "$warmup_request" "$workdir/shell.log" || true)"
failures=0

if [ "${settings_models:-0}" -gt 0 ]; then
  echo "PASS: settings env block won (${settings_models} model request(s) on its port)"
else
  echo "FAIL: no model request reached the settings env block port — injection would not route"
  sed 's/^/  received: /' "$workdir/settings.log"
  failures=$((failures + 1))
fi

if [ -n "$shell_others" ]; then
  echo "FAIL: requests other than the start-up warm-up reached the shell environment port — the env-priority assumption broke"
  printf '%s\n' "$shell_others" | sed 's/^/  received: /'
  failures=$((failures + 1))
elif [ "${shell_warmups:-0}" -gt 0 ]; then
  echo "PASS: shell environment lost (only the start-up warm-up, ${warmup_request}, on its port)"
else
  echo "PASS: shell environment lost (0 requests on its port)"
fi

if [ -e "$marker_session" ]; then
  echo "PASS: SessionStart hook ran"
else
  echo "FAIL: SessionStart hook did not run"
  failures=$((failures + 1))
fi

if [ -e "$marker_prompt" ]; then
  echo "PASS: UserPromptSubmit hook ran"
else
  echo "FAIL: UserPromptSubmit hook did not run"
  failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed: the installed claude no longer matches trajector's injection assumptions."
  exit 1
fi
echo "All checks passed."
