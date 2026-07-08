#!/bin/bash
# claude-semaphore state hook — writes per-session traffic-light state files
# consumed by the claude-semaphore tray app.
#
# Usage: state.sh <working|pre|attention|done|end>
# Claude Code delivers the hook event JSON on stdin.
#
# States: red = Claude needs your input, orange = working/idle, green = finished.
#
# Runs on macOS, Linux, and Windows (Git Bash, which Claude Code requires
# there anyway). Deliberately avoids jq — only sed/grep, present everywhere.

STATE_DIR="$HOME/.claude/semaphore"
mkdir -p "$STATE_DIR"

INPUT=$(cat 2>/dev/null)

# Extract a top-level string field from single-line hook JSON. Good enough
# for session_id/tool_name; not a general JSON parser.
json_field() {
  printf '%s' "$INPUT" | sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -1
}

SESSION=$(json_field session_id)
[ -n "$SESSION" ] || SESSION="unknown"
FILE="$STATE_DIR/$SESSION"

set_state() { printf '%s\n' "$1" > "$FILE"; }

case "$1" in
  working)
    set_state orange
    ;;
  pre)
    # AskUserQuestion means Claude is showing a question dialog — that is
    # "waiting on you", not "working".
    if [ "$(json_field tool_name)" = "AskUserQuestion" ]; then
      set_state red
    else
      set_state orange
    fi
    ;;
  attention)
    # The periodic idle "waiting for your input" notification must not
    # downgrade an already-finished (green) session back to red.
    if [ "$(cat "$FILE" 2>/dev/null)" = "green" ] && printf '%s' "$INPUT" | grep -qi 'waiting for your input'; then
      :
    else
      set_state red
    fi
    ;;
  done)
    set_state green
    ;;
  end)
    rm -f "$FILE"
    ;;
esac
exit 0
