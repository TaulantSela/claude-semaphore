package main

// Session model. Two sources describe each Claude Code session:
//
//  1. Claude Code's own session registry, ~/.claude/sessions/<pid>.json. Hosts
//     such as the VS Code extension write status "busy" | "waiting" | "idle"
//     there, plus waitingFor ("permission prompt" | "input needed") while a
//     dialog is open. It flips the moment a dialog opens AND the moment it is
//     answered, and it reflects auto mode and frontends that never deliver
//     Notification hooks. It is undocumented, so it is only an overlay: an
//     entry we cannot parse, or a status we do not know, falls back to (2).
//  2. The plugin's hook files, ~/.claude/semaphore/<session_id> (one state word)
//     with a <session_id>.meta sidecar holding the Claude Code pid and cwd.
//
// Display priority across sessions: red > fresh green > orange > idle. Green is
// "fresh" for greenFresh after the turn ended, unless you mark it seen, so a
// task that finished in one tab is not hidden by another tab that is still
// working, and a tab you finished with this morning does not stay green all day.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// Hook files untouched this long are ignored when the session's process
	// cannot be checked (no pid recorded, e.g. files from plugin <= 0.1.7).
	staleAfter = 12 * time.Hour

	// How long a finished session keeps the light green.
	greenFresh = 10 * time.Minute

	// Orange breathes while a session is provably busy (registry) or its hook
	// file was touched within this window.
	activeWindow = 3 * time.Minute
)

// Display states.
const (
	stRed    = "red"
	stGreen  = "green"
	stOrange = "orange"
	stIdle   = "idle"
	stNone   = "none" // no sessions at all
)

var priority = map[string]int{stRed: 0, stGreen: 1, stOrange: 2, stIdle: 3}

type hookFile struct {
	state string
	mtime time.Time
	pid   int
	cwd   string
}

type regEntry struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"` // unix ms
	Spare           bool   `json:"spare"`           // pre-warmed, not a real tab yet
}

type session struct {
	id       string
	name     string
	state    string
	waiting  string    // registry waitingFor, when red
	since    time.Time // when the session entered its state (best effort)
	finished bool      // the last turn ended normally (green, or green gone stale)
	breathe  bool      // orange that is actually doing something
}

// readHooks loads the plugin's per-session hook files.
func readHooks(dir string) map[string]hookFile {
	out := map[string]hookFile{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	metas := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if id, ok := strings.CutSuffix(name, ".meta"); ok {
			metas[id] = filepath.Join(dir, name)
			continue
		}
		// Skip sidecars (.pending, .meta, temp files) and the catch-all file
		// older plugin versions wrote when a hook carried no session_id.
		if strings.Contains(name, ".") || name == "unknown" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		line, _, _ := strings.Cut(string(raw), "\n")
		st := strings.TrimSpace(line)
		switch st {
		case stRed, stOrange, stGreen, stIdle:
		default:
			continue // empty or unknown: skip rather than guess
		}
		out[name] = hookFile{state: st, mtime: info.ModTime()}
	}
	for id, path := range metas {
		h, ok := out[id]
		if !ok {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			k, v, _ := strings.Cut(strings.TrimRight(line, "\r"), "=")
			switch k {
			case "pid":
				h.pid, _ = strconv.Atoi(v)
			case "cwd":
				h.cwd = unescapeJSON(v)
			}
		}
		out[id] = h
	}
	return out
}

// readRegistry loads Claude Code's session registry. live holds the entries
// whose process is running and whose status we understand (newest entry wins
// when a session id appears twice); dead holds session ids that only have
// entries for processes that are gone.
func readRegistry(dir string, alive func(int) bool) (live map[string]regEntry, dead map[string]bool) {
	live, dead = map[string]regEntry{}, map[string]bool{}
	running := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r regEntry
		if json.Unmarshal(raw, &r) != nil || r.SessionID == "" || r.PID <= 0 || r.Spare {
			continue
		}
		if !alive(r.PID) {
			dead[r.SessionID] = true
			continue
		}
		running[r.SessionID] = true
		switch r.Status {
		case "busy", "waiting", "idle":
		default:
			continue // unknown status: let the hook file speak for this session
		}
		if prev, ok := live[r.SessionID]; ok && prev.StatusUpdatedAt >= r.StatusUpdatedAt {
			continue
		}
		live[r.SessionID] = r
	}
	for id := range running {
		delete(dead, id) // resumed in a new process: the old entry is just litter
	}
	return live, dead
}

// buildSessions merges both sources into one list, sorted by display priority
// then recency. gone lists hook-file session ids whose process is known to be
// dead, so the caller can delete their files.
func buildSessions(hooks map[string]hookFile, live map[string]regEntry, regDead map[string]bool,
	alive func(int) bool, acked map[string]time.Time, now time.Time) (ss []session, gone []string) {

	for id, r := range live {
		h, hasHook := hooks[id]
		s := session{id: id, name: displayName(r.Name, r.Cwd, h.cwd, id)}
		updated := time.UnixMilli(r.StatusUpdatedAt)
		switch r.Status {
		case "waiting":
			s.state, s.waiting, s.since = stRed, r.WaitingFor, updated
		case "busy":
			s.state, s.since, s.breathe = stOrange, updated, true
		case "idle":
			// The registry cannot tell "just finished" from "idle since
			// yesterday"; the Stop hook can.
			if hasHook && h.state == stGreen {
				s.state, s.since, s.finished = stGreen, h.mtime, true
			} else {
				s.state, s.since = stIdle, updated
			}
		}
		ss = append(ss, s)
	}

	for id, h := range hooks {
		if _, ok := live[id]; ok {
			continue
		}
		if regDead[id] || (h.pid > 0 && !alive(h.pid)) {
			gone = append(gone, id)
			continue
		}
		if h.pid <= 0 && now.Sub(h.mtime) > staleAfter {
			continue
		}
		s := session{id: id, name: displayName("", h.cwd, "", id), state: h.state, since: h.mtime}
		switch h.state {
		case stOrange:
			s.breathe = now.Sub(h.mtime) < activeWindow
		case stGreen:
			s.finished = true
		}
		ss = append(ss, s)
	}

	for i := range ss {
		s := &ss[i]
		if s.state != stGreen {
			continue
		}
		seen, ok := acked[s.id]
		if now.Sub(s.since) > greenFresh || (ok && !seen.Before(s.since)) {
			s.state = stIdle // still listed as "finished Xm ago", but no longer green
		}
	}

	disambiguate(ss)
	sort.SliceStable(ss, func(i, j int) bool {
		if pi, pj := priority[ss[i].state], priority[ss[j].state]; pi != pj {
			return pi < pj
		}
		if !ss[i].since.Equal(ss[j].since) {
			return ss[i].since.After(ss[j].since)
		}
		return ss[i].id < ss[j].id
	})
	sort.Strings(gone)
	return ss, gone
}

// aggregate returns the state the tray shows and how many sessions share it.
func aggregate(ss []session) (string, int) {
	if len(ss) == 0 {
		return stNone, 0
	}
	best := stIdle
	for _, s := range ss {
		if priority[s.state] < priority[best] {
			best = s.state
		}
	}
	n := 0
	for _, s := range ss {
		if s.state == best {
			n++
		}
	}
	return best, n
}

func anyBreathing(ss []session) bool {
	for _, s := range ss {
		if s.state == stOrange && s.breathe {
			return true
		}
	}
	return false
}

// headline is the first menu line and the tooltip.
func headline(state string, n, total int) string {
	var s string
	switch state {
	case stRed:
		s = "Claude needs your input"
		if n > 1 {
			s = fmt.Sprintf("%d sessions need your input", n)
		}
	case stGreen:
		s = "Task finished"
		if n > 1 {
			s = fmt.Sprintf("%d tasks finished", n)
		}
	case stOrange:
		s = "Claude is working…"
		if n > 1 {
			s = fmt.Sprintf("%d sessions working…", n)
		}
	case stIdle:
		s = "Claude is idle"
	default:
		return "No active Claude sessions"
	}
	if total > 1 {
		s += fmt.Sprintf("  (%d sessions)", total)
	}
	return s
}

var stateEmoji = map[string]string{stRed: "🔴", stGreen: "🟢", stOrange: "🟠", stIdle: "⚪️"}

// sessionLine is one session's menu entry, e.g. "🟢  komshi — finished 3m ago".
func sessionLine(s session, now time.Time) string {
	var detail string
	switch {
	case s.state == stRed && s.waiting != "":
		detail = s.waiting
	case s.state == stRed:
		detail = "needs your input"
	case s.state == stOrange:
		detail = "working"
	case s.finished:
		detail = "finished " + ago(now.Sub(s.since))
	default:
		detail = "idle"
	}
	return fmt.Sprintf("%s  %s — %s", stateEmoji[s.state], s.name, detail)
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// displayName prefers the registry's session name (e.g. "bookslot-3f"), then
// the project folder, then a short session id.
func displayName(regName, regCwd, hookCwd, id string) string {
	if regName != "" {
		return regName
	}
	for _, cwd := range []string{regCwd, hookCwd} {
		if cwd != "" {
			if b := filepath.Base(filepath.Clean(strings.ReplaceAll(cwd, `\`, "/"))); b != "." && b != "/" {
				return b
			}
		}
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// disambiguate suffixes repeated names (two tabs in one folder) with a short id.
func disambiguate(ss []session) {
	count := map[string]int{}
	for _, s := range ss {
		count[s.name]++
	}
	for i := range ss {
		if count[ss[i].name] > 1 && len(ss[i].id) >= 4 {
			ss[i].name = fmt.Sprintf("%s (%s)", ss[i].name, ss[i].id[:4])
		}
	}
}

// unescapeJSON decodes the escapes a JSON string value can carry (the hook
// script copies cwd out of the hook payload verbatim, e.g. C:\\Users\\me).
func unescapeJSON(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var out string
	if json.Unmarshal([]byte(`"`+v+`"`), &out) == nil {
		return out
	}
	return v
}
