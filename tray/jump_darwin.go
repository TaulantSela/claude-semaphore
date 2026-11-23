package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procInfo returns pid's parent and executable path. ps prints the full path
// on macOS, spaces and all, after the parent pid.
func procInfo(pid int) (int, string, bool) {
	out, err := run(2*time.Second, "/bin/ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid))
	if err != nil {
		return 0, "", false
	}
	ppid, exe, _ := strings.Cut(strings.TrimSpace(out), " ")
	n, err := strconv.Atoi(ppid)
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimSpace(exe), true
}

// openFiles lists the files pid holds open.
func openFiles(pid int) []string {
	out, _ := run(3*time.Second, "/usr/sbin/lsof", "-w", "-n", "-P", "-Fn", "-p", strconv.Itoa(pid))
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "n/") {
			files = append(files, line[1:])
		}
	}
	return files
}

func ttyOf(pid int) string {
	out, err := run(2*time.Second, "/bin/ps", "-o", "tty=", "-p", strconv.Itoa(pid))
	t := strings.TrimSpace(out)
	if err != nil || t == "" || strings.HasPrefix(t, "?") {
		return ""
	}
	return "/dev/" + t
}

func jump(t target) error {
	switch {
	case t.url != "":
		// -a hands the link to the app that hosts the session rather than to
		// whichever app last registered the scheme.
		_, err := run(10*time.Second, "/usr/bin/open", "-a", t.app, t.url)
		return err
	case t.app == "":
		return nil
	}
	if script := tabScript(t.app); script != "" && t.tty != "" {
		// The first time, macOS asks whether the tray may control the
		// terminal; the minute covers that prompt.
		out, err := run(time.Minute, "/usr/bin/osascript", "-e", script, t.tty)
		if err == nil && strings.TrimSpace(out) == "ok" {
			return nil
		}
		// Not allowed, or no tab runs that tty: the app alone still helps.
	}
	_, err := run(10*time.Second, "/usr/bin/open", "-a", t.app)
	return err
}

// tabScript returns the AppleScript that selects the tab whose tty is its
// argument in the terminal app, or "" for apps it cannot script.
func tabScript(app string) string {
	switch strings.ToLower(filepath.Base(app)) {
	case "terminal.app":
		return terminalScript
	case "iterm.app", "iterm2.app":
		return itermScript
	}
	return ""
}

const terminalScript = `on run argv
	set t to item 1 of argv
	tell application "Terminal"
		repeat with w in windows
			repeat with b in tabs of w
				if tty of b is t then
					if miniaturized of w then set miniaturized of w to false
					set selected of b to true
					set index of w to 1
					activate
					return "ok"
				end if
			end repeat
		end repeat
	end tell
	return "missing"
end run`

const itermScript = `on run argv
	set t to item 1 of argv
	tell application id "com.googlecode.iterm2"
		repeat with w in windows
			repeat with b in tabs of w
				repeat with s in sessions of b
					if tty of s is t then
						select w
						select b
						select s
						activate
						return "ok"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	return "missing"
end run`
