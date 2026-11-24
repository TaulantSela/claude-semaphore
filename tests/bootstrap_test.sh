#!/bin/bash
# Scenario tests for plugin/scripts/bootstrap.sh: installing, updating and
# restarting the tray, including several sessions starting at once.
#
# The tray is a small C stand-in that, like the real one, holds a localhost
# port as its single-instance lock and runs as argv[0] = its path. It logs
# "<version> <pid>" when it takes the port. curl is a stub that serves those
# builds as release assets, slowly enough for runs to overlap; launchctl is a
# no-op. HOME points at a scratch folder, so nothing touches the real tray.
#
# Usage: tests/bootstrap_test.sh [path/to/bootstrap.sh]

set -u
cd "$(dirname "$0")/.."
SCRIPT=$(cd "$(dirname "${1:-plugin/scripts/bootstrap.sh}")" && pwd)/$(basename "${1:-plugin/scripts/bootstrap.sh}")
W=$(mktemp -d)
FAILS=0

export HOME="$W/home"
export FAKE_PORT=$((20000 + RANDOM % 20000))
export FAKE_LOG="$W/tray.log"
export FAKE_RELEASES="$W/releases"
export FAKE_CURL_LOG="$W/curl.log"
export FAKE_CURL_DELAY=0
export FAKE_LATEST=none
mkdir -p "$HOME/Library/LaunchAgents" "$FAKE_RELEASES" "$W/stubs" "$W/plugin/scripts" "$W/plugin/.claude-plugin"
: > "$FAKE_LOG"

BIN="$HOME/.claude/semaphore-tray/claude-semaphore"
BIN_RE=$(printf '%s' "$BIN" | sed 's/[][\.*^$+?(){}|]/\\&/g')
TRAY_RE="^$BIN_RE( |\$)"
trap 'pkill -9 -f "$TRAY_RE" 2>/dev/null; rm -rf "$W"' EXIT

cat > "$W/tray.c" <<'EOF'
#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <unistd.h>
int main(void) {
  int s = socket(AF_INET, SOCK_STREAM, 0), on = 1;
  struct sockaddr_in a = {0};
  a.sin_family = AF_INET;
  a.sin_port = htons(atoi(getenv("FAKE_PORT")));
  a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  setsockopt(s, SOL_SOCKET, SO_REUSEADDR, &on, sizeof on);
  if (bind(s, (struct sockaddr *)&a, sizeof a) != 0 || listen(s, 1) != 0)
    return 0; /* another copy runs */
  FILE *f = fopen(getenv("FAKE_LOG"), "a");
  fprintf(f, "%s %d\n", VERSION, (int)getpid());
  fclose(f);
  for (;;) pause();
}
EOF
for v in 0.3.0 0.3.1; do
  cc -DVERSION="\"$v\"" -o "$FAKE_RELEASES/$v" "$W/tray.c" || { echo "cc failed"; exit 1; }
done

cat > "$W/stubs/curl" <<'EOF'
#!/bin/bash
out= url=
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift 2 ;;
    --retry) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
echo "$url" >> "$FAKE_CURL_LOG"
sleep "$FAKE_CURL_DELAY"
case $url in
  */latest/download/*) v=$FAKE_LATEST ;;
  */download/v*) v=${url#*/download/v}; v=${v%%/*} ;;
esac
[ -f "$FAKE_RELEASES/$v" ] || exit 22
cat "$FAKE_RELEASES/$v" > "$out"
EOF
printf '#!/bin/sh\nexit 0\n' > "$W/stubs/launchctl"
chmod +x "$W/stubs/curl" "$W/stubs/launchctl"
export PATH="$W/stubs:$PATH"
cp "$SCRIPT" "$W/plugin/scripts/bootstrap.sh"

plugin_version() {
  printf '{\n  "name": "semaphore",\n  "version": "%s"\n}\n' "$1" > "$W/plugin/.claude-plugin/plugin.json"
}
bootstrap() { bash "$W/plugin/scripts/bootstrap.sh"; }

# settle waits for trays the script started to take the port and log.
settle() { sleep 0.5; }

running() { pgrep -f "$TRAY_RE" | sort | tr '\n' ' ' | sed 's/ $//'; }

# running_version prints the version of the one running tray, "none", or
# "N trays" when more than one runs.
running_version() {
  local pids n
  pids=$(running)
  n=$(echo "$pids" | wc -w | tr -d ' ')
  case $n in
    0) echo none ;;
    1) awk -v p="$pids" '$2 == p { v = $1 } END { print (v == "" ? "unlogged" : v) }' "$FAKE_LOG" ;;
    *) echo "$n trays" ;;
  esac
}

check() {
  local what=$1 got=$2 want=$3
  if [ "$got" = "$want" ]; then
    printf '  ok    %-58s %s\n' "$what" "$got"
  else
    printf '  FAIL  %-58s got %s, want %s\n' "$what" "$got" "$want"
    FAILS=$((FAILS + 1))
  fi
}

installed() { cat "$HOME/.claude/semaphore-tray/.version" 2>/dev/null || echo "<none>"; }
downloads() { wc -l < "$FAKE_CURL_LOG" 2>/dev/null | tr -d ' ' || echo 0; }
leftovers() { ls -A "$HOME/.claude/semaphore-tray" | grep -v -x -e claude-semaphore -e .version -e .autostart-installed | tr '\n' ' '; }

echo "install"
plugin_version 0.3.0
bootstrap; settle
check "first run downloads and starts the tray" "$(running_version)" 0.3.0
check "records the installed version" "$(installed)" 0.3.0

echo "restart"
pkill -f "$TRAY_RE"; sleep 0.3; : > "$FAKE_CURL_LOG"
bootstrap; settle
check "a stopped tray is started again" "$(running_version)" 0.3.0
check "without downloading anything" "$(downloads)" 0

echo "update while several sessions start at once"
plugin_version 0.3.1
# The gaps VS Code left between restored tabs' SessionStart hooks on the
# night this broke: each run began while earlier runs were downloading.
FAKE_CURL_DELAY=2
for gap in 1.3 1.3 0.4 0; do bootstrap & sleep "$gap"; done
wait; settle
check "the new tray runs, alone" "$(running_version)" 0.3.1
check "records the new version" "$(installed)" 0.3.1
check "no temp files or lock left behind" "$(leftovers)" ""
FAKE_CURL_DELAY=0

echo "older plugin"
before=$(running); : > "$FAKE_CURL_LOG"
plugin_version 0.3.0
bootstrap; settle
check "a session on an older plugin keeps the newer tray" "$(running_version)" 0.3.1
check "the same process" "$(running)" "$before"
check "and the recorded version" "$(installed)" 0.3.1
check "without downloading anything" "$(downloads)" 0

echo "failed download"
before=$(running)
plugin_version 0.3.2 # no such release, and no latest either
bootstrap; settle
check "the old tray keeps running" "$(running)" "$before"
check "the recorded version stays" "$(installed)" 0.3.1

echo "stale lock"
pkill -f "$TRAY_RE"; sleep 0.3
plugin_version 0.3.1
sleep 0 & dead=$!; wait $dead
mkdir -p "$HOME/.claude/semaphore-tray/.bootstrap.lock"
printf '%s' "$dead" > "$HOME/.claude/semaphore-tray/.bootstrap.lock/pid"
bootstrap; settle
check "a lock left by a dead run is taken over" "$(running_version)" 0.3.1

echo
[ "$FAILS" = 0 ] && echo "all passed" || { echo "$FAILS failed"; exit 1; }
