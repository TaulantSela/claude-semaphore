# 🔴🟠🟢 Claude Semaphore

A traffic light for [Claude Code](https://claude.com/claude-code) in your
system tray / menu bar — visible from **any** window, on macOS, Windows, and
Linux.

**▶ [See the live demo →](https://claude-semaphore.vercel.app/#demo)** — watch
the tray states animate (red beats like a heart, orange breathes).

Ever give Claude a task, switch to another window, and come back hoping it's
done — only to find it's been sitting there waiting for your permission?
Claude Semaphore tells you at a glance:

| Light | Meaning |
|-------|---------|
| 🔴 | Claude is **waiting for your input** — a permission prompt or question is open |
| 🟢 | **Task finished** — come back and collect (stays green for 10 minutes) |
| 🟠 | Claude is **working** |
| ⚪️ | Nothing running, or no Claude sessions open |

The active states animate so you can catch them from the corner of your eye:
🔴 beats like a heart with a soft glow until you respond, 🟠 gently breathes
while Claude works, and 🟢 / ⚪️ stay still.

Multiple sessions? The light shows the most urgent one: a session that needs
you, then one that just finished, then one that is working, so a busy tab
can't hide a tab that is done. On macOS a count beside the dot says how many
sessions share that state, and the menu lists every session:

```
2 sessions need your input  (4 sessions)
🔴  bookslot-3f — permission prompt
🔴  komshi-a1 — input needed
🟢  watchcrop-07 — finished 2m ago
🟠  portfolio-e7 — working
```

Green lasts 10 minutes after a turn ends. Click a finished session (or
**Mark finished as seen**) once you have looked at it.

## Install

Inside Claude Code:

```
/plugin marketplace add TaulantSela/claude-semaphore
/plugin install semaphore@claude-semaphore
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
   write one tiny state file per session to `~/.claude/semaphore/`, plus a
   sidecar with the session's process id and project folder.
2. **The tray app** (`tray/`, a single static Go binary using
   [fyne.io/systray](https://github.com/fyne-io/systray)) watches that
   directory and shows the aggregate state within milliseconds of a change.

Where Claude Code keeps its own session registry (`~/.claude/sessions/`,
written by the VS Code extension), the tray reads it as well. It reports a
dialog the moment it opens **and the moment you answer it**, including in
auto mode, which hooks alone cannot do. The registry is undocumented, so the
tray only uses entries it fully understands and falls back to the hook files
for everything else, including terminal sessions.

Sessions whose process has exited are dropped, so a closed tab or a crash
can't wedge the light; files without a process id (from older plugin
versions) are ignored after 12 hours. No network, no telemetry — the tray
reads local files, nothing else.

## Uninstall

```
/plugin uninstall semaphore@claude-semaphore
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

These apply to sessions without the session registry — in practice, Claude
Code in a terminal. In the VS Code extension the registry covers them.

- Claude Code has no "permission granted" hook. After you click **Allow**,
  red holds until the approved tool finishes (a long build stays red until it
  completes). After **No** or **Esc** on a dialog no hook fires at all, so red
  holds until your next prompt in that session.
- Red for permission dialogs triggers on the `PermissionRequest` hook, which
  fires the instant a dialog is about to appear. In auto-like permission
  modes the event is ignored (it can fire for calls the classifier then
  allows without a dialog,
  [#29212](https://github.com/anthropics/claude-code/issues/29212)); those
  modes fall back to the `Notification` hook, which terminals deliver after
  a ~6 s idle gate.
- Red is sticky against `PostToolUse`: tools started in parallel before a
  dialog appeared may finish while it waits, and their completion must not
  flip the light back to orange. Subagents are tracked separately, so one
  agent working on does not clear another agent's dialog.
- If Claude ends its turn by asking a question in plain text (no dialog), the
  light shows green first and turns red when the idle "waiting for your
  input" notification fires (~1 min).

## When the light looks wrong

```bash
~/.claude/semaphore-tray/claude-semaphore --status   # what the tray sees, per session
```

To trace every hook event, start Claude Code with `SEMAPHORE_LOG` set:

```bash
SEMAPHORE_LOG=/tmp/semaphore.log claude
```

Each line shows the event, the agent, the tool and the state it produced.

## Development

```bash
cd tray
go build -o claude-semaphore .   # needs Go 1.22+; macOS build needs Xcode CLT
./claude-semaphore
```

Tests:

```bash
(cd tray && go test ./...)   # session merging and priority
tests/state_test.sh          # hook script scenarios
```

Test the plugin without installing:

```bash
claude --plugin-dir ./plugin
```

`hooks.json` must only name hook events that every supported Claude Code
version knows: a build that meets an unknown event (for example
`PostToolBatch` on 2.1.97) silently skips **all** of the plugin's hooks.

Releases are built by GitHub Actions for darwin/linux/windows × amd64/arm64
when a `v*` tag is pushed:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

## License

[MIT](LICENSE)
