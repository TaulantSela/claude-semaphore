# Claude Semaphore

A traffic light for Claude Code in your menu bar (macOS) or system tray
(Windows, Linux). It turns red when a session needs your input, green when a
task has finished and orange while Claude is working. The menu lists every
session, and clicking one brings its tab forward.

## What it installs and runs

- **Hooks** write one small state file per session to `~/.claude/semaphore/`.
  Each file holds the light's state, the Claude Code process id, the project
  folder and the tools waiting for permission. The session's end deletes
  them.
- **`scripts/bootstrap.sh`** runs at session start. On first run, and when
  the plugin version changes, it downloads the tray app for your platform
  from this repository's GitHub Releases. It runs the download only if its
  SHA-256 matches the one pinned in [`SHA256SUMS`](SHA256SUMS), and deletes
  it otherwise. It then registers the tray to start at login (a launchd
  agent on macOS, an autostart entry on Linux, the Run registry key on
  Windows) and starts it. GitHub Actions builds the binaries from the Go
  source in the repository's `tray/` folder.
- **The tray app** reads those state files and Claude Code's session
  registry (`~/.claude/sessions/`). It makes no network requests and sends
  nothing anywhere. The only network access is the bootstrap's download from
  GitHub.

## More

Full documentation, uninstall steps and source:
<https://github.com/TaulantSela/claude-semaphore>
