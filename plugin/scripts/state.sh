#!/bin/bash
# claude-semaphore state hook — writes per-session traffic-light state files
# consumed by the claude-semaphore tray app.
#
# Usage: state.sh <action-hint>
# Claude Code delivers the hook event JSON on stdin. The event name embedded
# in the JSON (hook_event_name) takes precedence over the argv hint, so
# behavior updates apply even to sessions that captured an older hooks.json.
#
# Files, in $SEMAPHORE_STATE_DIR (default ~/.claude/semaphore):
#   <session_id>          one word: red | orange | green | idle
#   <session_id>.meta     pid=<Claude Code pid>, cwd=<project dir>; lets the tray
#                         drop sessions whose process died without SessionEnd
#   <session_id>.pending  open permission dialogs, one "<agent> <tool>" per line
#
# The tray prefers Claude Code's own session registry (~/.claude/sessions)
# where the host writes one; these files are the fallback and the only record
# of "this turn finished" (green).
#
# Runs on macOS, Linux, and Windows (Git Bash, which Claude Code requires
# there anyway). Deliberately avoids jq, and keeps forks to a minimum because
# Claude Code waits for this hook on every tool call.

# Byte-wise matching is all we need, and bash's string operations on a large
# payload are ~5x faster in the C locale than in a UTF-8 one.
export LC_ALL=C

STATE_DIR="${SEMAPHORE_STATE_DIR:-$HOME/.claude/semaphore}"
[ -d "$STATE_DIR" ] || mkdir -p "$STATE_DIR"

INPUT=$(cat 2>/dev/null)

# Read the fields we need from the part of the payload BEFORE tool_input.
# Claude Code emits the top-level fields (session_id, cwd,
# permission_mode, agent_id, hook_event_name, tool_name, source, ...) first,
# so cutting there keeps nested keys — an MCP tool's own "session_id" or
# "agent_id" argument, say — from being mistaken for ours, and keeps the scan
# small however large the tool output is. Keys inside JSON string values are
# escaped (\"key\") and never match.
HEAD=${INPUT%%\"tool_input\"*}
SESSION= EVENT= TOOL= MODE= NTYPE= AGENT= SRC= CWD= MSG=
while IFS= read -r kv; do
  k=${kv#\"}; k=${k%%\"*}
  v=${kv#*:}; v=${v#*\"}; v=${v%\"}
  case $k in
    session_id)        [ -n "$SESSION" ] || SESSION=$v ;;
    hook_event_name)   [ -n "$EVENT" ]   || EVENT=$v ;;
    tool_name)         [ -n "$TOOL" ]    || TOOL=$v ;;
    permission_mode)   [ -n "$MODE" ]    || MODE=$v ;;
    notification_type) [ -n "$NTYPE" ]   || NTYPE=$v ;;
    agent_id)          [ -n "$AGENT" ]   || AGENT=$v ;;
    source)            [ -n "$SRC" ]     || SRC=$v ;;
    cwd)               [ -n "$CWD" ]     || CWD=$v ;;
    message)           [ -n "$MSG" ]     || MSG=$v ;;
  esac
done <<EOF
$(printf '%s' "$HEAD" | grep -oE '"(session_id|hook_event_name|tool_name|permission_mode|notification_type|agent_id|source|cwd|message)"[[:space:]]*:[[:space:]]*"[^"]*"')
EOF

# Nothing to attribute the event to. (Older versions wrote these to a shared
# "unknown" file, which then never went away.)
[ -n "$SESSION" ] || exit 0

FILE="$STATE_DIR/$SESSION"
PEND="$FILE.pending"
META="$FILE.meta"
# Subagents report their own agent_id; the main conversation has none.
AG=${AGENT:-main}
CUR=
[ -f "$FILE" ] && { read -r CUR < "$FILE"; } 2>/dev/null

# Write via a temp file + rename so the tray never reads a half-written file.
put() { printf '%s\n' "$2" > "$1.$$" && mv -f "$1.$$" "$1"; }
set_state() { put "$FILE" "$1"; CUR=$1; }

# The Claude Code process that ran this hook: the nearest ancestor that is
# claude, the native installer's versioned binary (…/versions/2.1.204), or
# node/bun (npm build). Usually that is $PPID itself, or its parent when the
# hook runs under a `sh -c` wrapper; looking no further than that keeps a
# Claude session launched from inside another one from claiming the outer
# process. (CLAUDE_PID is no help: hooks only inherit it from whatever started
# claude.) Skipped on Windows, where Git Bash cannot see native parent
# processes; the tray then relies on the registry and a 12-hour staleness rule.
claude_pid() {
  case "$OSTYPE" in msys*|cygwin*|win*) return ;; esac
  local p=$PPID i=0 pp comm
  while [ "$i" -lt 3 ] && [ "${p:-0}" -gt 1 ]; do
    read -r pp comm <<PS
$(ps -o ppid= -o comm= -p "$p" 2>/dev/null)
PS
    [ -n "$pp" ] || return
    case "${comm##*/}" in
      claude|claude.exe|node|bun|[0-9]*.[0-9]*.[0-9]*) echo "$p"; return ;;
    esac
    p=$pp; i=$((i + 1))
  done
}

# Ledger of permission dialogs that are currently open, one "<agent> <tool>"
# per line. Claude Code fires no "permission granted" event, so an approved
# dialog is only observable as the PostToolUse of the tool it was gating, and
# PermissionRequest carries no tool_use_id, so agent + tool name is the best
# correlation key available. Keying by agent lets a subagent keep working
# without wiping a dialog another agent is still showing.
pend_add()   { printf '%s %s\n' "$AG" "$1" >> "$PEND"; }
pend_clear() { rm -f "$PEND"; }
pend_empty() { [ ! -s "$PEND" ]; }
# Drop the FIRST "<agent> <tool>" line; succeed only if one was dropped, so
# two parallel dialogs for the same tool must each be answered.
pend_take() {
  [ -s "$PEND" ] || return 1
  grep -qxF "$AG $1" "$PEND" 2>/dev/null || return 1
  awk -v t="$AG $1" 'hit==0 && $0==t { hit=1; next } { print }' "$PEND" > "$PEND.$$" &&
    mv -f "$PEND.$$" "$PEND"
}
# Drop every line of this agent: it is running tools again, so whatever it
# was asking about has been answered.
pend_drop_agent() {
  [ -s "$PEND" ] || return 0
  grep -q "^$AG " "$PEND" 2>/dev/null || return 0
  awk -v a="$AG " 'index($0, a) != 1' "$PEND" > "$PEND.$$" && mv -f "$PEND.$$" "$PEND"
}

# Record which process owns this session, once.
if [ ! -f "$META" ] && [ "$EVENT" != SessionEnd ]; then
  put "$META" "pid=$(claude_pid)
cwd=$CWD"
fi

# Map to an action: prefer the real event name, fall back to the argv hint.
case "$EVENT" in
  SessionStart)
    # A new, resumed or cleared session is idle until you prompt it. Only a
    # compaction happens mid-turn.
    if [ "$SRC" = compact ]; then ACTION=working; else ACTION=start; fi ;;
  UserPromptSubmit|PermissionDenied|ElicitationResult) ACTION=working ;;
  PreToolUse)        ACTION=pre ;;
  PostToolUse)       ACTION=post ;;
  PostToolUseFailure)
    # Esc while a tool ran ends the turn without a Stop event.
    case "$INPUT" in
      *'"is_interrupt":true'*) ACTION=interrupt ;;
      *) ACTION=post ;;
    esac ;;
  # No PostToolBatch: Claude Code builds that predate it (e.g. 2.1.97) skip a
  # plugin's hooks entirely when hooks.json names an event they don't know.
  # Denying a dialog or pressing Esc on it fires no hook at all, so red holds
  # until the next prompt there; registry hosts (VS Code) clear it at once.
  PermissionRequest) ACTION=permreq ;;
  Notification|Elicitation) ACTION=attention ;;
  Stop|StopFailure)  ACTION=done ;;
  SessionEnd)        ACTION=end ;;
  *)                 ACTION="$1" ;;
esac

# After the main turn has ended (green), a background subagent's tool calls
# must not repaint the session as working: green means "come and look".
sub_after_done() { [ "$AG" != main ] && [ "$CUR" = green ]; }

case "$ACTION" in
  start)
    pend_clear
    set_state idle
    ;;
  working)
    pend_clear
    set_state orange
    ;;
  pre)
    # AskUserQuestion is Claude showing a question dialog — that is "waiting
    # on you", not "working". Any other new tool call proves this agent is
    # unblocked; red clears only when no other agent still has a dialog open.
    # (PreToolUse always precedes its own call's PermissionRequest, so this
    # cannot drop a dialog that is about to open.)
    if [ "$TOOL" = AskUserQuestion ]; then
      pend_add AskUserQuestion
      set_state red
    else
      pend_drop_agent
      if pend_empty && ! sub_after_done; then set_state orange; fi
    fi
    ;;
  post)
    # The gated tool of an approved dialog completing is the only proof the
    # dialog was answered (there is no permission-granted event); clear red
    # when it closes the last open dialog. Tools launched earlier in parallel
    # can still FINISH while a dialog waits — they must not downgrade red.
    if pend_take "$TOOL"; then
      pend_empty && set_state orange
    elif [ "$CUR" != red ] && ! sub_after_done; then
      set_state orange
    fi
    ;;
  permreq)
    # A permission dialog is about to be shown — the primary red trigger.
    # It fires immediately and works in frontends that never deliver
    # Notification events (e.g. the VS Code extension's native UI).
    # Verified empirically: allowlisted and sandbox-auto-approved calls do
    # NOT fire this event, and acceptEdits mode auto-approves only edits —
    # its other tool calls show real dialogs. Only skip modes that never
    # show a dialog, plus auto mode, where the classifier's evaluation can
    # fire this without a dialog (github.com/anthropics/claude-code/29212).
    # In auto mode the tray reads red from Claude Code's registry instead.
    case "$MODE" in
      auto|bypassPermissions|dontAsk) : ;;
      *) pend_add "$TOOL"; set_state red ;;
    esac
    ;;
  attention)
    # Notifications carry a notification_type; only some types mean
    # "waiting on you". Elicitation events (no type field) fall through to
    # the default branch and turn red.
    case "$NTYPE" in
      auth_success|agent_completed)
        : ;;
      elicitation_complete|elicitation_response)
        set_state orange ;;
      idle_prompt)
        # Don't downgrade a finished (green) session for the idle reminder;
        # it does catch turns that ended with a plain-text question.
        [ "$CUR" = green ] || set_state red ;;
      *)
        # permission_prompt, elicitation_dialog, agent_needs_input, or an
        # older CLI without notification_type: legacy message-text guard.
        if [ "$CUR" = green ] && printf '%s' "$MSG" | grep -qi 'waiting for your input'; then
          :
        else
          set_state red
        fi ;;
    esac
    ;;
  interrupt)
    pend_clear
    set_state idle
    ;;
  done)
    # Stop, or StopFailure (an API error ended the turn): either way the turn
    # is over and the result — or the error — is waiting for you.
    pend_clear
    set_state green
    ;;
  end)
    rm -f "$FILE" "$PEND" "$META"
    CUR=
    ;;
esac

# Opt-in trace for diagnosing the light: export SEMAPHORE_LOG=/path/to/file
# (e.g. in the shell that launches claude) to log every event and its result.
if [ -n "${SEMAPHORE_LOG:-}" ]; then
  printf '%s %s %-18s %-10s %-16s -> %-6s %s\n' "$(date +%H:%M:%S)" "${SESSION:0:8}" \
    "$EVENT" "$AG" "${TOOL:--}" "${CUR:-gone}" "$(cat "$META" 2>/dev/null | tr '\n' ' ')" >> "$SEMAPHORE_LOG"
fi
exit 0
