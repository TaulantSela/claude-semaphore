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

Click a session to jump to its tab ([below](#jumping-to-a-session)); that
also marks a finished session as seen. Green lasts 10 minutes after a turn
ends, and **Mark finished as seen** clears it for every session at once.

## Install

Inside Claude Code:

```
/plugin marketplace add TaulantSela/claude-semaphore
/plugin install semaphore@claude-semaphore
```

Start a new Claude Code session and the traffic light appears in your tray
within a few seconds. On first run the plugin downloads the tray app for your
platform (~5 MB) from this repo's GitHub Releases and registers it to start
at login. It runs only a build whose SHA-256 matches the one pinned in the
plugin's [`SHA256SUMS`](plugin/SHA256SUMS); anything else is deleted unrun.
The builds come from this repo's source, compiled by
[GitHub Actions](.github/workflows/release.yml).

**Updates:** Claude Code does not update plugins from third-party
marketplaces unless you ask it to. To get new versions automatically, run
`/plugin`, open **Marketplaces**, select `claude-semaphore` and turn on
auto-update. To update by hand instead:

```bash
claude plugin marketplace update claude-semaphore
claude plugin update semaphore@claude-semaphore
```

The tray app updates itself to match at the next session start. If a newer
version has been out for a day and you are still on an older one, the tray's
menu shows **Update available**, which opens these instructions. To know,
the plugin asks GitHub for the latest release's version number at most once a
day: one request to github.com that sends nothing about you. Set
`SEMAPHORE_NO_UPDATE_CHECK=1` in your environment to turn it off.

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

## Jumping to a session

Click a session in the menu to bring it to the front:

- **VS Code** (also Insiders, Cursor, VSCodium): the session's Claude Code
  tab, in the window that hosts it, whether or not that tab is already
  open. The first time, VS Code asks *Allow 'Claude Code' extension to open
  this URI?*; tick **Do not ask me again for this extension**.
- **Terminal and iTerm2** on macOS: the tab running the session. macOS asks
  once whether Claude Semaphore may control the terminal.
- **Any other app** on macOS: the app running the session.

For VS Code, the tray sends the extension's own link,
`vscode://anthropic.claude-code/open?session=<id>`, addressed to the window
that hosts the session. It finds that window through the session's extension
host, which keeps its window's log folder open (on Windows, through VS Code's
logs instead). If it cannot tell the window, it only brings VS Code forward:
any other window would open the session a second time.

`claude-semaphore --status` shows where a click on each session goes, and
`claude-semaphore --open <session id>` does what the click does.

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
~/.claude/semaphore-tray/claude-semaphore --version  # which tray version is installed
```

To trace every hook event, start Claude Code with `SEMAPHORE_LOG` set:

```bash
SEMAPHORE_LOG=/tmp/semaphore.log claude
```

Each line shows the event, the agent, the tool and the state it produced.

If the tray acts like an older version after an update (before 0.3.1,
sessions starting together could leave the old tray running, so clicking a
session did nothing), restart it:

```bash
pkill -f "^$HOME/.claude/semaphore-tray/claude-semaphore\$"
nohup ~/.claude/semaphore-tray/claude-semaphore >/dev/null 2>&1 &
```

## Development

```bash
cd tray
go build -o claude-semaphore .   # needs Go 1.22+; macOS build needs Xcode CLT
./claude-semaphore
```

Tests:

```bash
(cd tray && go test ./...)   # session merging, priority, click targets
tests/state_test.sh          # hook script scenarios
tests/bootstrap_test.sh      # installing, updating and restarting the tray (needs cc)
```

Test the plugin without installing:

```bash
claude --plugin-dir ./plugin
```

`hooks.json` must only name hook events that every supported Claude Code
version knows: a build that meets an unknown event (for example
`PostToolBatch` on 2.1.97) silently skips **all** of the plugin's hooks.

To release, build first, then point the plugin at the build:

1. Merge the changes and push a tag on `main`. GitHub Actions builds the tray
   for darwin/linux/windows × amd64/arm64 and publishes a release with the
   binaries and their `SHA256SUMS`:

   ```bash
   git tag -s v0.3.2 -m v0.3.2 && git push origin v0.3.2
   ```

2. In one commit, bump `version` in `plugin/.claude-plugin/plugin.json` (the
   only place it is set) to the tag's version and replace
   `plugin/SHA256SUMS` with the release's. Users receive nothing until the
   version changes, so they never see a version whose binaries are missing:

   ```bash
   gh release download v0.3.2 -p SHA256SUMS -O plugin/SHA256SUMS --clobber
   ```

## License

[MIT](LICENSE)
