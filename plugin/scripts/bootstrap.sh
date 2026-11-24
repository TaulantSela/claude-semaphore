#!/bin/bash
# claude-semaphore bootstrap — runs async on SessionStart. Ensures the tray
# app is downloaded, registered to start at login, and running right now.
# Must never block or break a Claude session: every failure path exits 0.

set -u

REPO="TaulantSela/claude-semaphore"
BIN_DIR="$HOME/.claude/semaphore-tray"
STATE_DIR="$HOME/.claude/semaphore"
mkdir -p "$BIN_DIR" "$STATE_DIR" 2>/dev/null

case "$(uname -s)" in
  Darwin)               OS=darwin ;;
  Linux)                OS=linux ;;
  MINGW*|MSYS*|CYGWIN*) OS=windows ;;
  *)                    exit 0 ;;
esac

case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64)  ARCH=amd64 ;;
  *)             exit 0 ;;
esac

BIN="$BIN_DIR/claude-semaphore"
[ "$OS" = windows ] && BIN="$BIN.exe"
VERFILE="$BIN_DIR/.version"
PLIST="$HOME/Library/LaunchAgents/com.claude-semaphore.plist"

# The plugin version, read from the manifest beside this script. The binary is
# tagged with it so a plugin update also pulls a matching tray binary.
WANT_VER=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  "$(dirname "$0")/../.claude-plugin/plugin.json" 2>/dev/null | head -1)
HAVE_VER=$(cat "$VERFILE" 2>/dev/null)

# Several sessions start at once when an editor restores its tabs, and each
# runs this script. Let one do the work: the others exit and leave starting
# the tray to it. A holder that died (hook timeout, crash) leaves the lock
# behind, so a lock whose pid is gone, or that is older than 3 minutes (the
# hook times out after 120 s), is taken over.
LOCK="$BIN_DIR/.bootstrap.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  HOLDER=$(cat "$LOCK/pid" 2>/dev/null)
  if [ -z "$(find "$LOCK" -maxdepth 0 -mmin +3 2>/dev/null)" ] &&
     { [ -z "$HOLDER" ] || kill -0 "$HOLDER" 2>/dev/null; }; then
    exit 0 # held (no pid yet means its holder only just created it)
  fi
  rm -rf "$LOCK"
  mkdir "$LOCK" 2>/dev/null || exit 0
fi
printf '%s' "$$" > "$LOCK/pid" 2>/dev/null
TMP="$BIN.$$.tmp"
trap 'rm -rf "$LOCK" "$TMP"' EXIT

# The running tray, matched by its exact path: argv[0] is "$BIN", followed by
# nothing or its flags. A looser match (pkill -f "$BIN") also hits anything
# that merely mentions the path, such as another run's curl writing to
# "$BIN.tmp".
BIN_RE=$(printf '%s' "$BIN" | sed 's/[][\.*^$+?(){}|]/\\&/g')
TRAY_RE="^$BIN_RE( |\$)"

# stop_tray ends the running tray and waits until it is gone, so the next one
# can take the single-instance port.
stop_tray() {
  if [ "$OS" = windows ]; then
    taskkill //F //IM "$(basename "$BIN")" >/dev/null 2>&1
    sleep 1
    return 0
  fi
  pkill -f "$TRAY_RE" 2>/dev/null || return 0
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    pgrep -f "$TRAY_RE" >/dev/null 2>&1 || return 0
    sleep 0.3
  done
  pkill -9 -f "$TRAY_RE" 2>/dev/null
  sleep 0.3
}

# newer A B: version A is newer than version B (dotted numbers).
newer() {
  [ "$1" != "$2" ] &&
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)" = "$1" ]
}

# 1. Download the tray binary on first run, or when the plugin is newer than
#    the binary, so updating the plugin also updates the tray. Never go back:
#    a session still running an older plugin (its SessionStart fires again on
#    resume, /clear and compaction) must not swap an older tray in.
UPDATE=0
if [ ! -x "$BIN" ]; then
  UPDATE=1
elif [ -n "$WANT_VER" ] && [ "$WANT_VER" != "$HAVE_VER" ] &&
     { [ -z "$HAVE_VER" ] || newer "$WANT_VER" "$HAVE_VER"; }; then
  UPDATE=1
fi

if [ "$UPDATE" = 1 ]; then
  UPDATING=0
  [ -x "$BIN" ] && UPDATING=1

  ASSET="claude-semaphore-$OS-$ARCH"
  [ "$OS" = windows ] && ASSET="$ASSET.exe"

  # Prefer the asset tagged with the plugin version so binary and hooks match;
  # fall back to the latest release if that tag has no asset yet. The old
  # tray keeps running meanwhile, so a failed download leaves it be.
  GOT=""
  if [ -n "$WANT_VER" ] &&
     curl -fsSL --retry 2 -o "$TMP" \
       "https://github.com/$REPO/releases/download/v$WANT_VER/$ASSET" 2>/dev/null &&
     [ -s "$TMP" ]; then
    GOT="$WANT_VER"
  elif curl -fsSL --retry 2 -o "$TMP" \
       "https://github.com/$REPO/releases/latest/download/$ASSET" 2>/dev/null &&
     [ -s "$TMP" ]; then
    GOT="latest"
  fi

  if [ -n "$GOT" ]; then
    chmod +x "$TMP" 2>/dev/null
    # Windows locks a running .exe, so there the tray stops before the swap.
    # Elsewhere it stops after: whatever starts a tray from then on (launchd,
    # or the bootstrap of an older plugin version, which takes no lock)
    # starts the new binary.
    [ "$OS" = windows ] && [ "$UPDATING" = 1 ] && stop_tray
    if mv -f "$TMP" "$BIN" 2>/dev/null; then
      # Record the version only when we got the exact one asked for, so a
      # latest-fallback keeps trying for the pinned build next session.
      [ "$GOT" = "$WANT_VER" ] && printf '%s' "$WANT_VER" > "$VERFILE" 2>/dev/null
      if [ "$UPDATING" = 1 ] && [ "$OS" != windows ]; then
        # macOS: a launchd job pins a code requirement to the binary it was
        # first bootstrapped with and kills a replaced binary with
        # OS_REASON_CODESIGNING. Unload it, stop a tray started any other
        # way, then load it again so the requirement re-derives from the new
        # binary and RunAtLoad starts it.
        RELOAD=0
        [ "$OS" = darwin ] && [ -f "$PLIST" ] && RELOAD=1
        [ "$RELOAD" = 1 ] && launchctl bootout "gui/$(id -u)/com.claude-semaphore" 2>/dev/null
        stop_tray
        [ "$RELOAD" = 1 ] && launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null
      fi
    fi
  elif [ "$UPDATING" = 0 ]; then
    exit 0 # first-run download failed: nothing to launch
  fi
fi

# 2. Register login autostart, once.
MARKER="$BIN_DIR/.autostart-installed"
if [ ! -f "$MARKER" ]; then
  case "$OS" in
    darwin)
      PLIST="$HOME/Library/LaunchAgents/com.claude-semaphore.plist"
      cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.claude-semaphore</string>
    <key>ProgramArguments</key>
    <array>
        <string>$BIN</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>ProcessType</key>
    <string>Interactive</string>
</dict>
</plist>
EOF
      launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null
      ;;
    linux)
      mkdir -p "$HOME/.config/autostart" 2>/dev/null
      cat > "$HOME/.config/autostart/claude-semaphore.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Claude Semaphore
Comment=Traffic light for Claude Code state
Exec=$BIN
X-GNOME-Autostart-enabled=true
EOF
      ;;
    windows)
      WIN_BIN=$(cygpath -w "$BIN" 2>/dev/null) &&
        reg.exe add 'HKCU\Software\Microsoft\Windows\CurrentVersion\Run' \
          /v ClaudeSemaphore /t REG_SZ /d "\"$WIN_BIN\"" /f >/dev/null 2>&1
      ;;
  esac
  touch "$MARKER" 2>/dev/null
fi

# 3. Start it now. The app holds a localhost port as a single-instance lock
#    and exits immediately if another copy is already running, so spawning
#    unconditionally is safe.
case "$OS" in
  windows)
    WIN_BIN=$(cygpath -w "$BIN" 2>/dev/null) &&
      cmd.exe //c start '""' "$WIN_BIN" >/dev/null 2>&1 &
    ;;
  *)
    nohup "$BIN" >/dev/null 2>&1 &
    ;;
esac
exit 0
