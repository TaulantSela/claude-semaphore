package main

import (
	"os"
	"path/filepath"
	"testing"
)

type fakeProc struct {
	ppid int
	exe  string
}

func fakeProcs(table map[int]fakeProc, files map[int][]string, ttys map[int]string) procs {
	return procs{
		info: func(pid int) (int, string, bool) {
			p, ok := table[pid]
			return p.ppid, p.exe, ok
		},
		files: func(pid int) []string { return files[pid] },
		tty:   func(pid int) string { return ttys[pid] },
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeVSCode lays out a macOS VS Code bundle and returns it with the path of
// its extension host executable.
func fakeVSCode(t *testing.T) (app, exthost string) {
	app = filepath.Join(t.TempDir(), "Visual Studio Code.app")
	writeFile(t, filepath.Join(app, "Contents", "Resources", "app", "product.json"),
		`{"nameShort":"Code","urlProtocol":"vscode"}`)
	exthost = filepath.Join(app, "Contents", "Frameworks", "Code Helper (Plugin).app", "Contents", "MacOS", "Code Helper (Plugin)")
	return app, exthost
}

const sid = "dfa1e957-66dd-4426-9736-9fb6d50fefbe"

func TestVSCodeSessionOpensInItsOwnWindow(t *testing.T) {
	app, exthost := fakeVSCode(t)
	p := fakeProcs(map[int]fakeProc{
		500: {ppid: 400, exe: "/Users/me/.vscode/extensions/anthropic.claude-code-2.1.322-darwin-arm64/resources/native-binary/claude"},
		400: {ppid: 100, exe: exthost},
		100: {ppid: 1, exe: filepath.Join(app, "Contents", "MacOS", "Code")},
	}, map[int][]string{
		400: {
			"/Users/me/Library/Application Support/Code/User/globalStorage/state.vscdb",
			"/Users/me/Library/Application Support/Code/logs/20261110T192211/window3/exthost/exthost.log",
			"/Users/me/Library/Application Support/Code/logs/20261110T192211/window3/exthost/Anthropic.claude-code/Claude VSCode.log",
		},
	}, nil)
	got := resolve(session{id: sid, pid: 500, host: "claude-vscode"}, p, "")
	want := "vscode://anthropic.claude-code/open?session=" + sid + "&windowId=3"
	if got.url != want || got.app != app || got.window != 3 {
		t.Fatalf("target = %+v, want url %s in %s", got, want, app)
	}
	if s := got.String(); s != "Visual Studio Code, window 3" {
		t.Errorf("String() = %q", s)
	}
}

// Without the window id VS Code would deliver the link to its last focused
// window, which would resume the session as a second copy: bring the app
// forward instead.
func TestVSCodeSessionWithoutAKnownWindowGetsNoLink(t *testing.T) {
	app, exthost := fakeVSCode(t)
	p := fakeProcs(map[int]fakeProc{
		500: {ppid: 400, exe: "/x/claude"},
		400: {ppid: 100, exe: exthost},
	}, nil, nil)
	got := resolve(session{id: sid, pid: 500, host: "claude-vscode"}, p, t.TempDir())
	if got.url != "" || got.app != app {
		t.Fatalf("target = %+v, want just the app %s", got, app)
	}
}

func TestVSCodeWindowFromRendererLogsWhenFilesAreUnknown(t *testing.T) {
	_, exthost := fakeVSCode(t)
	cfg := t.TempDir()
	logs := filepath.Join(cfg, "Code", "logs")
	// An older run of VS Code, where the same pid once lived in window 1.
	writeFile(t, filepath.Join(logs, "20261101T090000", "window1", "renderer.log"),
		"2026-11-01 09:00:01.000 [info] Started local extension host with pid 400.\n")
	writeFile(t, filepath.Join(logs, "20261110T192211", "window1", "renderer.log"),
		"2026-11-10 19:22:13.134 [info] Started local extension host with pid 4000.\n")
	writeFile(t, filepath.Join(logs, "20261110T192211", "window2", "renderer.2.log"),
		"2026-11-16 14:23:20.833 [info] Started local extension host with pid 400.\n")
	writeFile(t, filepath.Join(logs, "20261110T192211", "window2", "renderer.log"), "rotated\n")
	// A later `code` CLI call leaves an empty run folder behind.
	if err := os.MkdirAll(filepath.Join(logs, "20261123T184601"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := fakeProcs(map[int]fakeProc{500: {ppid: 400, exe: "/x/claude"}, 400: {ppid: 100, exe: exthost}}, nil, nil)
	got := resolve(session{id: sid, pid: 500, host: "claude-vscode"}, p, cfg)
	if got.window != 2 {
		t.Fatalf("window = %d, want 2 (newest run, exact pid)", got.window)
	}

	// The same pid logged by two windows of the current run: no answer.
	writeFile(t, filepath.Join(logs, "20261110T192211", "window3", "renderer.log"),
		"2026-11-20 10:00:00.000 [info] Started local extension host with pid 400.\n")
	if got := resolve(session{id: sid, pid: 500, host: "claude-vscode"}, p, cfg); got.url != "" {
		t.Fatalf("ambiguous renderer logs gave %s", got.url)
	}
}

func TestWindowFromFilesMustBeUnambiguous(t *testing.T) {
	cases := []struct {
		files []string
		want  int
	}{
		{nil, 0},
		{[]string{"/a/logs/20261110T192211/window12/exthost/exthost.log"}, 12},
		{[]string{`C:\Users\me\AppData\Roaming\Code\logs\20261110T192211\window2\exthost\exthost.log`}, 2},
		{[]string{"/a/logs/r/window1/exthost/x.log", "/a/logs/r/window2/exthost/y.log"}, 0},
		{[]string{"/a/logs/r/window1/renderer.log"}, 0}, // not an extension host file
	}
	for _, c := range cases {
		if got := windowFromFiles(c.files); got != c.want {
			t.Errorf("windowFromFiles(%q) = %d, want %d", c.files, got, c.want)
		}
	}
}

// The link is the Claude Code extension's: a session some other host
// started under an extension host must not be resumed in a new tab.
func TestOnlyExtensionSessionsGetTheLink(t *testing.T) {
	app, exthost := fakeVSCode(t)
	p := fakeProcs(map[int]fakeProc{500: {ppid: 400, exe: "/x/claude"}, 400: {ppid: 100, exe: exthost}},
		map[int][]string{400: {"/a/logs/r/window3/exthost/exthost.log"}}, nil)
	for _, host := range []string{"", "cli", "sdk-ts"} {
		got := resolve(session{id: sid, pid: 500, host: host}, p, "")
		if got.url != "" || got.app != app {
			t.Errorf("host %q: target = %+v, want just the app", host, got)
		}
	}
}

func TestWrappedClaudeStillFindsItsWindow(t *testing.T) {
	_, exthost := fakeVSCode(t)
	p := fakeProcs(map[int]fakeProc{
		500: {ppid: 450, exe: "/x/claude"},
		450: {ppid: 400, exe: "/bin/bash"}, // claudeCode.claudeProcessWrapper
		400: {ppid: 100, exe: exthost},
	}, map[int][]string{400: {"/a/logs/r/window5/exthost/exthost.log"}}, nil)
	if got := resolve(session{id: sid, pid: 500, host: "claude-vscode"}, p, ""); got.window != 5 {
		t.Fatalf("target = %+v, want window 5", got)
	}
}

func TestTerminalSessionGoesToItsTab(t *testing.T) {
	term := "/System/Applications/Utilities/Terminal.app"
	p := fakeProcs(map[int]fakeProc{
		500: {ppid: 490, exe: "/Users/me/.local/share/claude/versions/2.1.204"},
		490: {ppid: 480, exe: "/bin/zsh"},
		480: {ppid: 470, exe: "/usr/bin/login"},
		470: {ppid: 1, exe: term + "/Contents/MacOS/Terminal"},
	}, nil, map[int]string{500: "/dev/ttys003"})
	got := resolve(session{id: sid, pid: 500}, p, "")
	if got.app != term || got.tty != "/dev/ttys003" || got.url != "" {
		t.Fatalf("target = %+v", got)
	}
	if s := got.String(); s != "Terminal, /dev/ttys003" {
		t.Errorf("String() = %q", s)
	}
}

func TestNowhereToGo(t *testing.T) {
	p := fakeProcs(map[int]fakeProc{500: {ppid: 490, exe: "/x/claude"}, 490: {ppid: 1, exe: "/usr/bin/tmux"}}, nil, nil)
	for _, s := range []session{{id: sid}, {id: sid, pid: 500}} {
		if got := resolve(s, p, ""); got != (target{}) {
			t.Errorf("resolve(%+v) = %+v, want nothing", s, got)
		}
	}
	if s := (target{}).String(); s != "nowhere to go" {
		t.Errorf("String() = %q", s)
	}
}

func TestEditorOfReadsProductJSON(t *testing.T) {
	// Linux: resources next to the executable.
	dir := filepath.Join(t.TempDir(), "code")
	writeFile(t, filepath.Join(dir, "resources", "app", "product.json"), `{"nameShort":"Code - Insiders","urlProtocol":"vscode-insiders"}`)
	if ed, ok := editorOf(filepath.Join(dir, "code")); !ok || ed.scheme != "vscode-insiders" || ed.name != "Code - Insiders" {
		t.Errorf("linux layout: %+v %v", ed, ok)
	}
	// Windows: resources in a per-version folder.
	win := filepath.Join(t.TempDir(), "Cursor")
	writeFile(t, filepath.Join(win, "a1b2c3", "resources", "app", "product.json"), `{"nameShort":"Cursor","urlProtocol":"cursor"}`)
	if ed, ok := editorOf(filepath.Join(win, "Cursor.exe")); !ok || ed.scheme != "cursor" {
		t.Errorf("versioned layout: %+v %v", ed, ok)
	}
	// Not an editor, or a product.json whose scheme could not be a URL scheme.
	bad := filepath.Join(t.TempDir(), "bad")
	writeFile(t, filepath.Join(bad, "resources", "app", "product.json"), `{"urlProtocol":"x y"}`)
	for _, exe := range []string{"/bin/zsh", filepath.Join(bad, "bad"), ""} {
		if ed, ok := editorOf(exe); ok {
			t.Errorf("editorOf(%q) = %+v", exe, ed)
		}
	}
}

func TestSessionsCarryTheirProcess(t *testing.T) {
	ss := build(
		map[string]hookFile{"h": {state: stOrange, mtime: now, pid: 77}},
		map[string]regEntry{"r": {PID: 88, SessionID: "r", Status: "busy", Entrypoint: "claude-vscode", StatusUpdatedAt: ms(now)}},
	)
	for _, s := range ss {
		switch s.id {
		case "h":
			if s.pid != 77 || s.host != "" {
				t.Errorf("hook session = %+v", s)
			}
		case "r":
			if s.pid != 88 || s.host != "claude-vscode" {
				t.Errorf("registry session = %+v", s)
			}
		}
	}
}
