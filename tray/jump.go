package main

// Jumping to a session: clicking one in the menu brings its tab forward.
//
//   - VS Code and its forks. The Claude Code extension handles the link
//     <scheme>://anthropic.claude-code/open?session=<id>: it reveals the
//     session's tab, or reopens it if the tab was closed. VS Code hands such a
//     link to its last focused window unless the link names one with
//     windowId=<N>, and a window that does not host the session would resume
//     it as a second copy. So the link only goes out with the hosting
//     window's id; when that is unknown, the app is just brought forward.
//     The id comes from the session's extension host, which keeps its
//     window's log folder (<user data>/logs/<run>/window<N>/exthost/) open,
//     or, where open files cannot be listed, from the renderer log line that
//     recorded the extension host's pid.
//   - Terminal and iTerm2 on macOS: the tab whose tty runs the session.
//   - Anything else on macOS: the app that hosts the session.
//
// The per-OS pieces (procInfo, openFiles, ttyOf, jump) live in jump_*.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const claudeExtension = "anthropic.claude-code"

// target is where a session lives and how to bring it forward.
type target struct {
	app    string // app bundle (macOS) or executable hosting the session
	url    string // link that reveals the session in its editor window
	window int    // that editor window's id
	tty    string // terminal device running the session
}

func (t target) String() string {
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(t.app), ".app"), ".exe")
	switch {
	case t.url != "":
		return fmt.Sprintf("%s, window %d", name, t.window)
	case t.tty != "":
		return fmt.Sprintf("%s, %s", name, t.tty)
	case t.app != "":
		return name
	}
	return "nowhere to go"
}

// procs is the part of the process table the lookup reads; tests fake it.
type procs struct {
	info  func(pid int) (ppid int, exe string, ok bool)
	files func(pid int) []string // paths the process holds open; nil if unknown
	tty   func(pid int) string   // its terminal device; "" if none
}

var system = procs{info: procInfo, files: openFiles, tty: ttyOf}

type proc struct {
	pid int
	exe string
}

// ancestors returns up to n processes above pid, nearest first.
func (p procs) ancestors(pid, n int) []proc {
	var out []proc
	ppid, _, ok := p.info(pid)
	for ok && ppid > 1 && len(out) < n {
		var next int
		var exe string
		if next, exe, ok = p.info(ppid); ok {
			out = append(out, proc{ppid, exe})
			ppid = next
		}
	}
	return out
}

// resolve works out where session s lives. userConfig is the folder that
// holds editors' user data (os.UserConfigDir). The zero target means there is
// nowhere to go.
func resolve(s session, p procs, userConfig string) target {
	if s.pid <= 0 {
		return target{}
	}
	chain := p.ancestors(s.pid, 8)
	if s.host == "claude-vscode" {
		// The extension starts claude directly, or through the wrapper set
		// in claudeCode.claudeProcessWrapper, so its host is close by.
		for _, a := range chain[:min(len(chain), 3)] {
			ed, ok := editorOf(a.exe)
			if !ok {
				continue
			}
			t := target{app: ed.app}
			if n := windowOf(a.pid, ed, p, userConfig); n > 0 {
				t.window = n
				t.url = fmt.Sprintf("%s://%s/open?session=%s&windowId=%d",
					ed.scheme, claudeExtension, url.QueryEscape(s.id), n)
			}
			return t
		}
	}
	// A terminal session: the nearest app above it is the terminal (or the
	// editor whose integrated terminal runs it).
	for _, a := range chain {
		if app := bundleOf(a.exe); app != "" {
			return target{app: app, tty: p.tty(s.pid)}
		}
	}
	return target{}
}

// editor is VS Code or a fork (Insiders, Cursor, VSCodium, …), known by the
// product.json it ships.
type editor struct {
	app    string // bundle (macOS) or executable
	scheme string // product.json urlProtocol, e.g. "vscode"
	name   string // product.json nameShort, e.g. "Code": names its user data folder
}

var schemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

func editorOf(exe string) (editor, bool) {
	var products []string
	app := bundleOf(exe)
	if app != "" {
		products = []string{filepath.Join(app, "Contents", "Resources", "app", "product.json")}
	} else if exe != "" {
		app = exe
		dir := filepath.Dir(exe)
		products = []string{filepath.Join(dir, "resources", "app", "product.json")}
		// Some Windows installs keep resources in a per-version folder.
		more, _ := filepath.Glob(filepath.Join(dir, "*", "resources", "app", "product.json"))
		products = append(products, more...)
	}
	for _, f := range products {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var p struct {
			URLProtocol string `json:"urlProtocol"`
			NameShort   string `json:"nameShort"`
		}
		if json.Unmarshal(raw, &p) == nil && schemeRe.MatchString(p.URLProtocol) {
			return editor{app: app, scheme: p.URLProtocol, name: p.NameShort}, true
		}
	}
	return editor{}, false
}

// bundleOf returns the outermost .app bundle that contains exe, or "".
var bundleRe = regexp.MustCompile(`^(.*?\.app)[\\/]`)

func bundleOf(exe string) string {
	if m := bundleRe.FindStringSubmatch(exe); m != nil {
		return m[1]
	}
	return ""
}

// windowOf returns the id of the editor window whose extension host is pid,
// or 0 when it cannot be sure.
func windowOf(pid int, ed editor, p procs, userConfig string) int {
	if n := windowFromFiles(p.files(pid)); n > 0 {
		return n
	}
	if userConfig == "" || ed.name == "" {
		return 0
	}
	return windowFromRendererLogs(filepath.Join(userConfig, ed.name, "logs"), pid)
}

// exthostLogRe matches a file in a window's extension host log folder.
var exthostLogRe = regexp.MustCompile(`[\\/]logs[\\/][^\\/]+[\\/]window(\d+)[\\/]exthost[\\/]`)

// windowFromFiles reads the window id off the log files an extension host
// holds open. They must all agree.
func windowFromFiles(files []string) int {
	found := 0
	for _, f := range files {
		m := exthostLogRe.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || (found != 0 && n != found) {
			return 0
		}
		found = n
	}
	return found
}

// windowFromRendererLogs finds the window whose renderer logged "Started
// local extension host with pid <pid>." in the running instance's logs. The
// line can rotate out of a long-lived window's logs, and a pid can be reused,
// so anything but exactly one window is no answer.
func windowFromRendererLogs(logs string, pid int) int {
	// Run folders are timestamps (20261110T192211). The running instance's
	// is the newest one with windows: every `code` CLI call adds an empty one.
	runs, _ := os.ReadDir(logs) // sorted by name, so oldest first
	var windows []string
	for i := len(runs) - 1; i >= 0 && len(windows) == 0; i-- {
		if runs[i].IsDir() {
			windows, _ = filepath.Glob(filepath.Join(logs, runs[i].Name(), "window*"))
		}
	}
	needle := []byte(fmt.Sprintf(" extension host with pid %d.", pid))
	found := 0
	for _, w := range windows {
		n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(w), "window"))
		if err != nil {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(w, "renderer*.log"))
		for _, f := range files {
			if raw, err := os.ReadFile(f); err == nil && bytes.Contains(raw, needle) {
				if found != 0 && found != n {
					return 0
				}
				found = n
				break
			}
		}
	}
	return found
}

// goTo brings session s forward. It shells out, so it runs off the tray's
// event loop.
func goTo(s session) error {
	cfg, _ := os.UserConfigDir()
	return jump(resolve(s, system, cfg))
}

// run executes a helper command and returns its output.
func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second // an app it launches must not hold our pipe open
	out, err := cmd.Output()
	return string(out), err
}
