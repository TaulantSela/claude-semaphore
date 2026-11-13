#!/bin/bash
# Scenario tests for plugin/scripts/state.sh. Feeds hook payloads shaped like
# Claude Code's (top-level fields first, then tool_input/tool_response) and
# checks the state file after each step.
#
# Usage: tests/state_test.sh

set -u
cd "$(dirname "$0")/.."
SCRIPT=plugin/scripts/state.sh
export SEMAPHORE_STATE_DIR
SEMAPHORE_STATE_DIR=$(mktemp -d)
trap 'rm -rf "$SEMAPHORE_STATE_DIR"' EXIT

S=sess-1
FAILS=0

# ev <event> [tool] [extra JSON fields placed after tool_name] [agent] [mode]
ev() {
  local event=$1 tool=${2:-} extra=${3:-} agent=${4:-} mode=${5:-default} j
  j="{\"session_id\":\"$S\",\"transcript_path\":\"/t/$S.jsonl\",\"cwd\":\"/p/bookslot\",\"permission_mode\":\"$mode\""
  [ -n "$agent" ] && j="$j,\"agent_id\":\"$agent\",\"agent_type\":\"general-purpose\""
  j="$j,\"hook_event_name\":\"$event\""
  [ -n "$tool" ] && j="$j,\"tool_name\":\"$tool\""
  [ -n "$extra" ] && j="$j,$extra"
  j="$j}"
  printf '%s' "$j" | bash "$SCRIPT"
}

expect() {
  local want=$1 what=$2 got
  got=$(cat "$SEMAPHORE_STATE_DIR/$S" 2>/dev/null || echo "<none>")
  if [ "$got" = "$want" ]; then
    printf '  ok    %-62s %s\n' "$what" "$got"
  else
    printf '  FAIL  %-62s got %s, want %s\n' "$what" "$got" "$want"
    FAILS=$((FAILS + 1))
  fi
}

TI='"tool_input":{"command":"npm test"},"tool_use_id":"toolu_1"'

echo "lifecycle"
ev SessionStart "" '"source":"startup"';            expect idle   "new tab is idle, not working"
[ -f "$SEMAPHORE_STATE_DIR/$S.meta" ] && grep -q '^cwd=/p/bookslot$' "$SEMAPHORE_STATE_DIR/$S.meta" &&
  echo "  ok    meta sidecar records cwd" || { echo "  FAIL  meta sidecar"; FAILS=$((FAILS + 1)); }
ev UserPromptSubmit;                                  expect orange "prompt submitted"
ev SessionStart "" '"source":"compact"';            expect orange "auto-compact mid-turn stays working"
ev Stop;                                              expect green  "turn finished"
ev Notification "" '"message":"Claude is waiting for your input","notification_type":"idle_prompt"'
                                                      expect green  "idle reminder does not downgrade green"
ev UserPromptSubmit; ev StopFailure "" '"error":"rate_limit"'
                                                      expect green  "API error ended the turn"

echo "permission dialogs"
ev UserPromptSubmit
ev PreToolUse Bash "$TI"
ev PermissionRequest Bash "$TI";                      expect red    "dialog shown"
ev PostToolUse Read '"tool_input":{"file_path":"/a"},"tool_response":"x"'
                                                      expect red    "parallel tool finishing keeps red"
ev PostToolUse Bash "$TI,\"tool_response\":{\"stdout\":\"ok\"}"
                                                      expect orange "approved tool completed"
ev PreToolUse Bash "$TI"; ev PermissionRequest Bash "$TI"
ev PostToolUseFailure Bash "$TI,\"error\":\"exit 1\",\"is_interrupt\":false"
                                                      expect orange "approved tool failed"
ev PreToolUse Bash "$TI"; ev PermissionRequest Bash "$TI"
ev UserPromptSubmit;                                  expect orange "denied (no hook fires): next prompt clears red"
ev PreToolUse Bash "$TI"; ev PermissionRequest Bash "$TI"
ev PostToolUseFailure Bash "$TI,\"error\":\"interrupted\",\"is_interrupt\":true"
                                                      expect idle   "Esc while the tool ran"
ev UserPromptSubmit; ev PermissionRequest Bash "$TI" "" auto
                                                      expect orange "auto mode: PermissionRequest ignored"

echo "subagents"
ev UserPromptSubmit
ev PermissionRequest Bash "$TI";                      expect red    "main agent asks"
ev PreToolUse Read '"tool_input":{"file_path":"/a"}' agent-7
                                                      expect red    "subagent working does not clear main's dialog"
ev PostToolUse Read '"tool_input":{"file_path":"/a"},"tool_response":"x"' agent-7
                                                      expect red    "subagent tool finishing does not clear it"
ev PreToolUse Edit '"tool_input":{"file_path":"/a"}'; expect orange "main agent unblocked"
ev PermissionRequest Bash "$TI" agent-7;              expect red    "subagent asks"
ev PreToolUse Read '"tool_input":{"file_path":"/a"}' agent-8
                                                      expect red    "sibling subagent does not clear it"
ev PreToolUse Read '"tool_input":{"file_path":"/b"}' agent-7
                                                      expect orange "asking subagent unblocked"
ev PreToolUse AskUserQuestion '"tool_input":{"questions":[]}'
                                                      expect red    "question dialog"
ev PreToolUse Read '"tool_input":{"file_path":"/a"}' agent-7
                                                      expect red    "subagent does not clear main's question"
ev PostToolUse AskUserQuestion '"tool_input":{"questions":[]},"tool_response":{}'
                                                      expect orange "question answered"
ev Stop
ev PreToolUse Bash "$TI" agent-bg;                    expect green  "background agent after Stop keeps green"
ev PreToolUse Bash "$TI";                             expect orange "main resumes work"

echo "robustness"
ev Stop
ev PreToolUse mcp__tasks__get '"tool_input":{"agent_id":"other","session_id":"evil"}' agent-bg
                                                      expect green  "nested agent_id/session_id ignored"
[ ! -e "$SEMAPHORE_STATE_DIR/evil" ] && echo "  ok    no file for a nested session_id" ||
  { echo "  FAIL  wrote a file for a nested session_id"; FAILS=$((FAILS + 1)); }
printf '%s' '{"hook_event_name":"PreToolUse","tool_name":"Bash"}' | bash "$SCRIPT"
[ ! -e "$SEMAPHORE_STATE_DIR/unknown" ] && echo "  ok    payload without session_id writes nothing" ||
  { echo "  FAIL  wrote unknown"; FAILS=$((FAILS + 1)); }
ls "$SEMAPHORE_STATE_DIR" | grep -E '\.[0-9]+$' >/dev/null &&
  { echo "  FAIL  temp files left behind"; FAILS=$((FAILS + 1)); } || echo "  ok    no temp files left behind"

BIG=$(python3 -c 'print("x\\\" y " * 100000)')
start=$(python3 -c 'import time; print(time.time())')
for _ in 1 2 3 4 5; do
  ev PostToolUse Bash "\"tool_input\":{\"command\":\"cat big\"},\"tool_response\":{\"stdout\":\"$BIG\"}"
done
python3 -c "import time; print('  info  600 KB PostToolUse: %.0f ms avg' % ((time.time() - $start) * 200))"
expect orange "large payload parsed"

ev SessionEnd
[ -z "$(ls "$SEMAPHORE_STATE_DIR")" ] && echo "  ok    SessionEnd removes every file" ||
  { echo "  FAIL  left: $(ls "$SEMAPHORE_STATE_DIR")"; FAILS=$((FAILS + 1)); }

echo
[ "$FAILS" = 0 ] && echo "all passed" || { echo "$FAILS failed"; exit 1; }
