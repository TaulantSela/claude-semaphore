# 🔴🟠🟢 Claude Semaphore

A traffic light for [Claude Code](https://claude.com/claude-code) in your
system tray / menu bar — visible from **any** window, on macOS, Windows, and
Linux.

Ever give Claude a task, switch to another window, and come back hoping it's
done — only to find it's been sitting there waiting for your permission?
Claude Semaphore tells you at a glance:

| Light | Meaning |
|-------|---------|
| 🔴 | Claude is **waiting for your input** — a permission prompt or question is open |
| 🟠 | Claude is **working** (or a session is idle) |
| 🟢 | **Task finished** — come back and collect |
| ⚪️ | No active Claude sessions |

Multiple sessions? Any session that needs you wins (red); otherwise the most
recently active session decides, so an idle window from this morning can't
hide your fresh green.

## Install

Inside Claude Code:

```
/plugin marketplace add TaulantSela/claude-code-semaphore
/plugin install semaphore@claude-code-semaphore
```

Start a new Claude Code session and the traffic light appears in your tray
within a few seconds. On first run the plugin downloads the tray app for your
platform (~5 MB) from this repo's GitHub Releases and registers it to start
at login.

**Requirements:** `curl` (preinstalled on macOS/Linux; ships with Git Bash on
Windows, which Claude Code already requires there).

## How it works

Two small parts:

1. **Hooks** (shipped by the plugin) fire on Claude Code lifecycle events —
   prompt submitted, tool running, permission needed, response finished — and
   write one tiny state file per session to `~/.claude/semaphore/`.
2. **The tray app** (`tray/`, a single static Go binary using
   [fyne.io/systray](https://github.com/fyne-io/systray)) polls that
   directory once a second and shows the aggregate state. No network, no
   telemetry — it reads local files, nothing else.

The tray menu offers **Reset to idle** and **Quit**. Session files untouched
for 12 hours are ignored, so crashed sessions can't wedge the light.

## Uninstall

```
/plugin uninstall semaphore@claude-code-semaphore
```

Then remove the app pieces:

```bash
# macOS
launchctl bootout gui/$(id -u)/com.claude-semaphore 2>/dev/null
rm -f ~/Library/LaunchAgents/com.claude-semaphore.plist
rm -rf ~/.claude/semaphore-tray ~/.claude/semaphore

# Linux
rm -f ~/.config/autostart/claude-semaphore.desktop
rm -rf ~/.claude/semaphore-tray ~/.claude/semaphore

# Windows (Git Bash)
reg.exe delete 'HKCU\Software\Microsoft\Windows\CurrentVersion\Run' /v ClaudeSemaphore /f
rm -rf ~/.claude/semaphore-tray ~/.claude/semaphore
```

## Known quirks

- A brief red flash can appear when Claude's permission classifier evaluates
  a command it then auto-allows; it self-corrects on the next tool event.
- If Claude ends its turn by asking a question in plain text (no dialog), the
  light shows green first and turns red when the idle "waiting for your
  input" notification fires (~1 min).
- During a long-running command no hook events fire, so a background session
  finishing meanwhile can briefly show green; red always wins regardless.

## Development

```bash
cd tray
go build -o claude-semaphore .   # needs Go 1.22+; macOS build needs Xcode CLT
./claude-semaphore
```

Test the plugin without installing:

```bash
claude --plugin-dir ./plugin
```

Releases are built by GitHub Actions for darwin/linux/windows × amd64/arm64
when a `v*` tag is pushed:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## License

[MIT](LICENSE)
