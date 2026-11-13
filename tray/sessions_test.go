package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 13, 15, 0, 0, 0, time.UTC)

func ms(t time.Time) int64 { return t.UnixMilli() }

func allAlive(int) bool { return true }

func build(hooks map[string]hookFile, live map[string]regEntry) []session {
	ss, _ := buildSessions(hooks, live, map[string]bool{}, allAlive, map[string]time.Time{}, now)
	return ss
}

func stateOf(t *testing.T, ss []session, id string) string {
	t.Helper()
	for _, s := range ss {
		if s.id == id {
			return s.state
		}
	}
	t.Fatalf("session %s missing from %+v", id, ss)
	return ""
}

// The headline bug: with "newest file wins", a busy tab hid a finished one.
func TestFinishedTabIsNotHiddenByBusyTab(t *testing.T) {
	ss := build(map[string]hookFile{
		"done": {state: stGreen, mtime: now.Add(-2 * time.Minute)},
		"busy": {state: stOrange, mtime: now}, // newer
	}, nil)
	if got, n := aggregate(ss); got != stGreen || n != 1 {
		t.Fatalf("aggregate = %s/%d, want green/1", got, n)
	}
}

func TestGreenGoesStaleAndCanBeMarkedSeen(t *testing.T) {
	hooks := map[string]hookFile{
		"old":   {state: stGreen, mtime: now.Add(-greenFresh - time.Minute)},
		"fresh": {state: stGreen, mtime: now.Add(-time.Minute)},
		"busy":  {state: stOrange, mtime: now},
	}
	ss := build(hooks, nil)
	if s := stateOf(t, ss, "old"); s != stIdle {
		t.Errorf("stale green = %s, want idle", s)
	}
	if got, _ := aggregate(ss); got != stGreen {
		t.Errorf("aggregate = %s, want green (fresh one)", got)
	}

	acked := map[string]time.Time{"fresh": now}
	ss, _ = buildSessions(hooks, nil, nil, allAlive, acked, now)
	if got, _ := aggregate(ss); got != stOrange {
		t.Errorf("after marking seen aggregate = %s, want orange", got)
	}
	// A new finish after the ack is green again.
	hooks["fresh"] = hookFile{state: stGreen, mtime: now.Add(time.Second)}
	ss, _ = buildSessions(hooks, nil, nil, allAlive, acked, now.Add(2*time.Second))
	if s := stateOf(t, ss, "fresh"); s != stGreen {
		t.Errorf("re-finished session = %s, want green", s)
	}
}

// Measured: after Allow the hook file stays red until the approved tool
// finishes; the registry flips to busy at once and must win.
func TestRegistryBeatsStickyHookRed(t *testing.T) {
	ss := build(
		map[string]hookFile{"s": {state: stRed, mtime: now}},
		map[string]regEntry{"s": {PID: 1, SessionID: "s", Status: "busy", StatusUpdatedAt: ms(now)}},
	)
	if s := stateOf(t, ss, "s"); s != stOrange {
		t.Fatalf("state = %s, want orange", s)
	}
}

// Auto mode in VS Code never produced red from hooks; the registry does.
func TestRegistryWaitingIsRedWithReason(t *testing.T) {
	ss := build(
		map[string]hookFile{"s": {state: stOrange, mtime: now}},
		map[string]regEntry{"s": {PID: 1, SessionID: "s", Status: "waiting", WaitingFor: "permission prompt", StatusUpdatedAt: ms(now)}},
	)
	if got, _ := aggregate(ss); got != stRed {
		t.Fatalf("aggregate = %s, want red", got)
	}
	if line := sessionLine(ss[0], now); line != "🔴  s — permission prompt" {
		t.Errorf("line = %q", line)
	}
}

func TestRegistryIdleUsesHookToTellFinishedFromIdle(t *testing.T) {
	reg := map[string]regEntry{
		"a": {PID: 1, SessionID: "a", Status: "idle", StatusUpdatedAt: ms(now)},
		"b": {PID: 2, SessionID: "b", Status: "idle", StatusUpdatedAt: ms(now)},
		"c": {PID: 3, SessionID: "c", Status: "idle", StatusUpdatedAt: ms(now)},
	}
	ss := build(map[string]hookFile{
		"a": {state: stGreen, mtime: now.Add(-time.Minute)}, // Stop fired
		"b": {state: stRed, mtime: now},                     // dialog dismissed with Esc
	}, reg)
	if s := stateOf(t, ss, "a"); s != stGreen {
		t.Errorf("a = %s, want green", s)
	}
	if s := stateOf(t, ss, "b"); s != stIdle {
		t.Errorf("b = %s, want idle (registry says nobody is waiting)", s)
	}
	if s := stateOf(t, ss, "c"); s != stIdle {
		t.Errorf("c = %s, want idle (registry-only session)", s)
	}
}

func TestDeadSessionsAreDroppedAndReported(t *testing.T) {
	alive := func(pid int) bool { return pid == 100 }
	hooks := map[string]hookFile{
		"live":    {state: stOrange, mtime: now, pid: 100},
		"killed":  {state: stRed, mtime: now, pid: 200},            // SIGTERM, no SessionEnd
		"crashed": {state: stRed, mtime: now},                      // no pid, registry knows it died
		"legacy":  {state: stOrange, mtime: now.Add(-time.Hour)},   // no pid, recent: keep
		"ancient": {state: stRed, mtime: now.Add(-13 * time.Hour)}, // no pid, stale: ignore
	}
	ss, gone := buildSessions(hooks, nil, map[string]bool{"crashed": true}, alive, nil, now)
	if len(gone) != 2 || gone[0] != "crashed" || gone[1] != "killed" {
		t.Errorf("gone = %v, want [crashed killed]", gone)
	}
	if len(ss) != 2 {
		t.Errorf("sessions = %+v, want live + legacy", ss)
	}
	if got, _ := aggregate(ss); got != stOrange {
		t.Errorf("aggregate = %s, want orange — a dead tab's red must not stick", got)
	}
}

func TestPriorityAndCounts(t *testing.T) {
	ss := build(map[string]hookFile{
		"r1": {state: stRed, mtime: now},
		"r2": {state: stRed, mtime: now},
		"g":  {state: stGreen, mtime: now},
		"o":  {state: stOrange, mtime: now},
	}, nil)
	if got, n := aggregate(ss); got != stRed || n != 2 {
		t.Fatalf("aggregate = %s/%d, want red/2", got, n)
	}
	if h := headline(stRed, 2, 4); h != "2 sessions need your input  (4 sessions)" {
		t.Errorf("headline = %q", h)
	}
	if ss[0].state != stRed || ss[len(ss)-1].state != stOrange {
		t.Errorf("not sorted by priority: %+v", ss)
	}
	if got, _ := aggregate(nil); got != stNone {
		t.Errorf("empty aggregate = %s", got)
	}
}

func TestBreathingOnlyWhenActuallyBusy(t *testing.T) {
	quiet := build(map[string]hookFile{"s": {state: stOrange, mtime: now.Add(-time.Hour)}}, nil)
	if anyBreathing(quiet) {
		t.Error("an orange hook file untouched for an hour should hold still")
	}
	// A long build fires no hooks, but the registry still says busy.
	busy := build(map[string]hookFile{"s": {state: stOrange, mtime: now.Add(-time.Hour)}},
		map[string]regEntry{"s": {PID: 1, SessionID: "s", Status: "busy", StatusUpdatedAt: ms(now.Add(-time.Hour))}})
	if !anyBreathing(busy) {
		t.Error("a registry-busy session should breathe")
	}
}

func TestNamesAreDisambiguated(t *testing.T) {
	ss := build(map[string]hookFile{
		"aaaa1111": {state: stOrange, mtime: now, cwd: "/p/bookslot"},
		"bbbb2222": {state: stOrange, mtime: now, cwd: "/p/bookslot"},
		"cccc3333": {state: stOrange, mtime: now, cwd: `C:\\Users\\me\\komshi`},
	}, nil)
	names := map[string]string{}
	for _, s := range ss {
		names[s.id] = s.name
	}
	if names["aaaa1111"] != "bookslot (aaaa)" || names["bbbb2222"] != "bookslot (bbbb)" {
		t.Errorf("duplicate names not disambiguated: %v", names)
	}
	if names["cccc3333"] != "komshi" {
		t.Errorf("windows cwd name = %q", names["cccc3333"])
	}
}

func TestReadersParseFilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("s1", "red\n")
	write("s1.meta", "pid=4242\ncwd=/p/bookslot\n")
	write("s1.pending", "main Bash\n")
	write("s1.123", "orange\n") // temp file mid-rename
	write("s2", "")             // truncated: skip
	write("unknown", "red\n")   // legacy catch-all: skip
	hooks := readHooks(dir)
	if len(hooks) != 1 || hooks["s1"].state != stRed || hooks["s1"].pid != 4242 || hooks["s1"].cwd != "/p/bookslot" {
		t.Fatalf("hooks = %+v", hooks)
	}

	reg := t.TempDir()
	os.WriteFile(filepath.Join(reg, "10.json"), []byte(`{"pid":10,"sessionId":"x","status":"waiting","waitingFor":"input needed","statusUpdatedAt":5,"name":"bookslot-3f"}`), 0o644)
	os.WriteFile(filepath.Join(reg, "11.json"), []byte(`{"pid":11,"sessionId":"y","status":"busy"}`), 0o644)                     // dead pid
	os.WriteFile(filepath.Join(reg, "12.json"), []byte(`{"pid":12,"sessionId":"z","status":"idle","spare":true}`), 0o644)        // spare
	os.WriteFile(filepath.Join(reg, "13.json"), []byte(`{"pid":13,"sessionId":"w","status":"shell"}`), 0o644)                    // unknown status
	os.WriteFile(filepath.Join(reg, "14.json"), []byte(`{"pid":14,"sessionId":"w","status":"busy","statusUpdatedAt":1}`), 0o644) // same session, dead pid
	os.WriteFile(filepath.Join(reg, "nope.key"), []byte(`secret`), 0o644)
	live, dead := readRegistry(reg, func(pid int) bool { return pid == 10 || pid == 12 || pid == 13 })
	if len(live) != 1 || live["x"].WaitingFor != "input needed" || live["x"].Name != "bookslot-3f" {
		t.Errorf("live = %+v", live)
	}
	if !dead["y"] || dead["w"] || len(dead) != 1 {
		t.Errorf("dead = %v, want only y (w still runs in pid 13)", dead)
	}
}
