#!/bin/bash
# Scenario tests for plugin/scripts/bootstrap.sh: installing, updating and
# restarting the tray, including several sessions starting at once.
#
# The tray is a small C stand-in that, like the real one, holds a localhost
# port as its single-instance lock and runs as argv[0] = its path. It logs
# "<version> <pid>" when it takes the port. curl is a stub that serves those
# builds as release assets, slowly enough for runs to overlap; launchctl is a
# no-op. Each plugin version pins its build's SHA-256 in SHA256SUMS, as the
# real plugin does. HOME points at a scratch folder, so nothing touches the
# real tray.
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
export FAKE_LATEST= # version GitHub's /releases/latest redirects to; empty = fails
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
# 0.3.2 stands in for a release asset that is not the build the plugin pinned.
for v in 0.3.0 0.3.1 0.3.2; do
  cc -DVERSION="\"$v\"" -o "$FAKE_RELEASES/$v" "$W/tray.c" || { echo "cc failed"; exit 1; }
done

cat > "$W/stubs/curl" <<'EOF'
#!/bin/bash
out= url=
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift 2 ;;
    -w|--retry|--max-time) shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
echo "$url" >> "$FAKE_CURL_LOG"
case $url in
  */releases/latest)
    [ -n "$FAKE_LATEST" ] || exit 22
    printf 'https://github.com/o/r/releases/tag/v%s' "$FAKE_LATEST"
    exit 0 ;;
esac
sleep "$FAKE_CURL_DELAY"
case $url in
  */download/v*) v=${url#*/download/v}; v=${v%%/*} ;;
esac
[ -f "$FAKE_RELEASES/$v" ] || exit 22
cat "$FAKE_RELEASES/$v" > "$out"
EOF
printf '#!/bin/sh\nexit 0\n' > "$W/stubs/launchctl"
chmod +x "$W/stubs/curl" "$W/stubs/launchctl"
export PATH="$W/stubs:$PATH"
cp "$SCRIPT" "$W/plugin/scripts/bootstrap.sh"

case "$(uname -s)" in Darwin) OS=darwin ;; *) OS=linux ;; esac
case "$(uname -m)" in arm64|aarch64) ARCH=arm64 ;; *) ARCH=amd64 ;; esac
ASSET="claude-semaphore-$OS-$ARCH"
sha() { { command -v sha256sum >/dev/null && sha256sum "$1" || shasum -a 256 "$1"; } | awk '{ print $1 }'; }

# plugin_version VERSION [PIN]: the plugin at VERSION, pinning the checksum of
# release build PIN (default VERSION; "none" ships no SHA256SUMS; a version
# without a build pins a checksum nothing has).
plugin_version() {
  local pin=${2:-$1} sums="$W/plugin/SHA256SUMS"
  printf '{\n  "name": "semaphore",\n  "version": "%s"\n}\n' "$1" > "$W/plugin/.claude-plugin/plugin.json"
  rm -f "$sums"
  if [ -f "$FAKE_RELEASES/$pin" ]; then
    printf '%s  %s\n' "$(sha "$FAKE_RELEASES/$pin")" "$ASSET" > "$sums"
  elif [ "$pin" != none ]; then
    printf '%064d  %s\n' 0 "$ASSET" > "$sums"
  fi
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
downloads() { grep -c /releases/download/ "$FAKE_CURL_LOG" 2>/dev/null || true; }
checks() { grep -c /releases/latest "$FAKE_CURL_LOG" 2>/dev/null || true; }
leftovers() { ls -A "$HOME/.claude/semaphore-tray" | grep -v -x -e claude-semaphore -e .version -e .autostart-installed -e .latest -e .update-check | tr '\n' ' '; }

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
plugin_version 0.3.3 # no such release
bootstrap; settle
check "the old tray keeps running" "$(running)" "$before"
check "the recorded version stays" "$(installed)" 0.3.1

echo "checksums"
plugin_version 0.3.2 0.3.1 # the 0.3.2 asset is not the build the plugin pinned
bootstrap; settle
check "a build that does not match the pinned checksum is refused" "$(running)" "$before"
check "the recorded version stays" "$(installed)" 0.3.1
check "and the refused download is deleted" "$(leftovers)" ""
: > "$FAKE_CURL_LOG"
plugin_version 0.3.2 none
bootstrap; settle
check "without a pinned checksum nothing is downloaded" "$(downloads)" 0
check "the old tray keeps running" "$(running)" "$before"
plugin_version 0.3.2
bootstrap; settle
check "the pinned build installs" "$(running_version)" 0.3.2

echo "stale lock"
pkill -f "$TRAY_RE"; sleep 0.3
sleep 0 & dead=$!; wait $dead
mkdir -p "$HOME/.claude/semaphore-tray/.bootstrap.lock"
printf '%s' "$dead" > "$HOME/.claude/semaphore-tray/.bootstrap.lock/pid"
bootstrap; settle
check "a lock left by a dead run is taken over" "$(running_version)" 0.3.2

echo "update check"
TRAYDIR="$HOME/.claude/semaphore-tray"
mtime() { stat -f %m "$1" 2>/dev/null || stat -c %Y "$1"; }
rm -f "$TRAYDIR/.update-check" "$TRAYDIR/.latest"; : > "$FAKE_CURL_LOG"
FAKE_LATEST=0.3.9 bootstrap
check "records the latest release's version" "$(cat "$TRAYDIR/.latest" 2>/dev/null)" 0.3.9
touch -t 202601010000 "$TRAYDIR/.latest"; first=$(mtime "$TRAYDIR/.latest")
FAKE_LATEST=0.4.0 bootstrap
check "asks at most once a day" "$(checks)" 1
rm -f "$TRAYDIR/.update-check"
FAKE_LATEST=0.3.9 bootstrap
check "an unchanged version leaves the file's age alone" "$(mtime "$TRAYDIR/.latest")" "$first"
rm -f "$TRAYDIR/.update-check"
FAKE_LATEST= bootstrap
check "a failed check keeps what it knew" "$(cat "$TRAYDIR/.latest")" 0.3.9
rm -f "$TRAYDIR/.update-check"; : > "$FAKE_CURL_LOG"
SEMAPHORE_NO_UPDATE_CHECK=1 FAKE_LATEST=0.4.0 bootstrap
check "SEMAPHORE_NO_UPDATE_CHECK=1 turns it off" "$(checks)" 0

echo
[ "$FAILS" = 0 ] && echo "all passed" || { echo "$FAILS failed"; exit 1; }
