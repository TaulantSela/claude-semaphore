#!/bin/bash
# claude-semaphore bootstrap — runs async on SessionStart. Ensures the tray
# app is downloaded, registered to start at login, and running right now.
# Must never block or break a Claude session: every failure path exits 0.

set -u

REPO="TaulantSela/claude-code-semaphore"
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

# 1. Download the tray binary on first run.
if [ ! -x "$BIN" ]; then
  ASSET="claude-semaphore-$OS-$ARCH"
  [ "$OS" = windows ] && ASSET="$ASSET.exe"
  URL="https://github.com/$REPO/releases/latest/download/$ASSET"
  curl -fsSL --retry 2 -o "$BIN.tmp" "$URL" 2>/dev/null || { rm -f "$BIN.tmp"; exit 0; }
  chmod +x "$BIN.tmp" 2>/dev/null
  mv "$BIN.tmp" "$BIN" 2>/dev/null || exit 0
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
