// claude-semaphore — a cross-platform system tray traffic light for Claude Code.
//
// Shows the aggregate state of every running Claude Code session:
//
//	red    — a session is waiting for your input (permission prompt or question)
//	green  — a session finished its task within the last few minutes
//	orange — a session is working
//	grey   — nothing running, or no sessions at all
//
// Priority is red > green > orange > grey, so a tab that finished is not
// hidden by another tab that is still busy. The menu lists every session.
//
// State comes from Claude Code's session registry where the host writes one
// (the VS Code extension does), falling back to the plugin's hook files; see
// sessions.go. Both directories are watched, so the light follows an event
// within milliseconds; a slow poll backs that up and ages the green state.
//
// `claude-semaphore --status` prints the sessions and the aggregate instead of
// starting the tray, which helps when the light looks wrong.
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/systray"
	"github.com/fsnotify/fsnotify"
)

const (
	// Binding this port is the cross-platform single-instance lock: a second
	// copy fails to bind and exits silently, so the bootstrap hook can spawn
	// the app unconditionally.
	singleInstanceAddr = "127.0.0.1:47816"

	// Safety net under the file watcher; also ages green and re-checks pids.
	pollInterval = 2 * time.Second

	// File events arrive in bursts (temp file + rename, several hooks at once);
	// coalesce them into one refresh.
	debounce = 30 * time.Millisecond

	// Session rows in the menu; anything beyond is summarised in the last row.
	maxSlots = 10
)

var stateDir, registryDir = func() (string, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".claude", "semaphore"), filepath.Join(home, ".claude", "sessions")
}()

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--status" {
		printStatus()
		return
	}
	lock, err := net.Listen("tcp", singleInstanceAddr)
	if err != nil {
		os.Exit(0) // another instance is already running
	}
	defer lock.Close()

	_ = os.MkdirAll(stateDir, 0o755)
	systray.Run(onReady, func() {})
}

// tracker holds the tray's view of the world between refreshes.
type tracker struct {
	acked    map[string]time.Time // session id → when you marked it seen
	sessions []session
	state    string
	count    int
	breathe  bool
}

func (t *tracker) refresh(now time.Time) {
	hooks := readHooks(stateDir)
	live, dead := readRegistry(registryDir, pidAlive)
	ss, gone := buildSessions(hooks, live, dead, pidAlive, t.acked, now)
	for _, id := range gone {
		for _, suffix := range []string{"", ".pending", ".meta"} {
			_ = os.Remove(filepath.Join(stateDir, id+suffix))
		}
		delete(t.acked, id)
	}
	t.sessions = ss
	t.state, t.count = aggregate(ss)
	t.breathe = anyBreathing(ss)
}

func (t *tracker) markSeen(now time.Time, only string) {
	for _, s := range t.sessions {
		if s.finished && (only == "" || s.id == only) {
			t.acked[s.id] = now
		}
	}
}

func printStatus() {
	t := &tracker{acked: map[string]time.Time{}}
	now := time.Now()
	t.refresh(now)
	fmt.Printf("%s — %s\n", t.state, headline(t.state, t.count, len(t.sessions)))
	for _, s := range t.sessions {
		fmt.Printf("  %s   [%s]\n", sessionLine(s, now), s.id)
	}
}

func onReady() {
	buildFrames()

	// macOS: a non-empty title forces the status button into NSImageLeft, the
	// colour-preserving image mode, so the coloured dot survives even on macOS
	// versions where an icon-only item renders as a monochrome mask. A single
	// space shows no glyph — just the dot; " 2" adds a count beside it.
	// SetTitle is a no-op on Windows and tooltip-only on Linux.
	isMac := runtime.GOOS == "darwin"
	if isMac {
		systray.SetTitle(" ")
	}

	header := systray.AddMenuItem(headline(stNone, 0, 0), "")
	header.Disable()
	slots := make([]*systray.MenuItem, maxSlots)
	for i := range slots {
		slots[i] = systray.AddMenuItem("", "Click a finished session to mark it seen")
		slots[i].Hide()
	}
	systray.AddSeparator()
	seenItem := systray.AddMenuItem("Mark finished as seen", "Stop showing green for tasks you have already looked at")
	resetItem := systray.AddMenuItem("Reset to idle", "Clear the plugin's state files")
	quitItem := systray.AddMenuItem("Quit", "")

	clicks := make(chan int)
	for i, it := range slots {
		go func(i int, ch chan struct{}) {
			for range ch {
				clicks <- i
			}
		}(i, it.ClickedCh)
	}

	t := &tracker{acked: map[string]time.Time{}}
	shown := "" // state currently painted
	frame := 0  // animation frame within the current state

	// Every systray call is a main-thread round trip (and a D-Bus layout
	// update on Linux), and the poll re-renders every couple of seconds, so
	// only touch what actually changed.
	texts := map[*systray.MenuItem]string{}
	setText := func(it *systray.MenuItem, s string) {
		if texts[it] != s {
			texts[it] = s
			it.SetTitle(s)
		}
	}
	visible := map[*systray.MenuItem]bool{}
	setVisible := func(it *systray.MenuItem, on bool) {
		if v, known := visible[it]; known && v == on {
			return
		}
		visible[it] = on
		if on {
			it.Show()
		} else {
			it.Hide()
		}
	}
	seenEnabled := true
	title, tooltip := " ", ""
	painted := ""
	slotIDs := make([]string, maxSlots)

	animating := func() bool {
		return t.state == stRed || (t.state == stOrange && t.breathe)
	}
	draw := func() {
		icons := frameIcons[t.state]
		idx := 0
		if animating() {
			idx = frame % len(icons)
		} // else frame 0: a steady, fully bright dot
		if key := fmt.Sprintf("%s/%d", t.state, idx); key != painted {
			painted = key
			systray.SetIcon(icons[idx])
		}
	}
	render := func(now time.Time) {
		if t.state != shown {
			shown, frame = t.state, 0 // restart the new state's animation
		}
		head := headline(t.state, t.count, len(t.sessions))
		if head != tooltip {
			tooltip = head
			systray.SetTooltip(head)
		}
		setText(header, head)
		if isMac {
			want := " "
			if t.count > 1 && t.state != stIdle && t.state != stNone {
				want = fmt.Sprintf(" %d", t.count)
			}
			if want != title {
				title = want
				systray.SetTitle(title)
			}
		}
		for i, it := range slots {
			slotIDs[i] = ""
			switch {
			case i < len(t.sessions) && (i < maxSlots-1 || len(t.sessions) == maxSlots):
				setText(it, sessionLine(t.sessions[i], now))
				slotIDs[i] = t.sessions[i].id
				setVisible(it, true)
			case i == maxSlots-1 && len(t.sessions) > maxSlots:
				setText(it, fmt.Sprintf("…and %d more", len(t.sessions)-(maxSlots-1)))
				setVisible(it, true)
			default:
				setVisible(it, false)
			}
		}
		anyFinished := false
		for _, s := range t.sessions {
			anyFinished = anyFinished || s.state == stGreen
		}
		if anyFinished != seenEnabled {
			seenEnabled = anyFinished
			if anyFinished {
				seenItem.Enable()
			} else {
				seenItem.Disable()
			}
		}
		draw()
	}

	t.refresh(time.Now())
	render(time.Now())

	go func() {
		poll := time.NewTicker(pollInterval)
		defer poll.Stop()

		// Watch both directories so a state change reaches the light at once.
		// If the watcher cannot start, the poll alone keeps things working.
		var events <-chan fsnotify.Event
		var errs <-chan error
		watched := map[string]bool{}
		w, err := fsnotify.NewWatcher()
		if err == nil {
			events, errs = w.Events, w.Errors
			defer w.Close()
		}
		watch := func() {
			if w == nil {
				return
			}
			for _, d := range []string{stateDir, registryDir} {
				if !watched[d] && w.Add(d) == nil {
					watched[d] = true // the registry dir may only appear later
				}
			}
		}
		watch()

		settle := time.NewTimer(time.Hour)
		settle.Stop()

		// The animation ticker exists only while something is animating. When
		// it is off, animC is nil — a nil channel blocks forever in select —
		// so a static tray costs nothing between events.
		var anim *time.Ticker
		var animC <-chan time.Time
		var animEvery time.Duration
		syncAnim := func() {
			every := time.Duration(0)
			if animating() {
				every = animInterval[t.state]
			}
			if every == animEvery {
				return
			}
			if anim != nil {
				anim.Stop()
				anim, animC = nil, nil
			}
			if every > 0 {
				anim = time.NewTicker(every)
				animC = anim.C
			}
			animEvery = every
		}
		update := func() {
			now := time.Now()
			t.refresh(now)
			render(now)
			syncAnim()
		}
		syncAnim()

		for {
			select {
			case ev := <-events:
				if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && (ev.Name == stateDir || ev.Name == registryDir) {
					delete(watched, ev.Name) // directory itself went away; re-add on poll
				}
				settle.Reset(debounce)
			case <-errs:
			case <-settle.C:
				update()
			case <-poll.C:
				watch()
				update()
			case <-animC:
				frame++
				draw()
			case i := <-clicks:
				if id := slotIDs[i]; id != "" {
					t.markSeen(time.Now(), id)
					update()
				}
			case <-seenItem.ClickedCh:
				t.markSeen(time.Now(), "")
				update()
			case <-resetItem.ClickedCh:
				entries, _ := os.ReadDir(stateDir)
				for _, e := range entries {
					_ = os.Remove(filepath.Join(stateDir, e.Name()))
				}
				t.markSeen(time.Now(), "")
				update()
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
