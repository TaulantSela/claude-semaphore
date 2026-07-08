// claude-semaphore — a cross-platform system tray traffic light for Claude Code.
//
// Reads per-session state files written by the plugin's hook script into
// ~/.claude/semaphore/ and shows the aggregate state:
//
//	red    — Claude is waiting for your input (permission prompt or question)
//	orange — Claude is working, or a session is idle
//	green  — task finished
//	gray   — no active Claude sessions
//
// Aggregation: any red session wins; otherwise the most recently active
// session speaks, so a stale idle session in another window cannot mask a
// freshly finished task.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
)

const (
	// Binding this port is the cross-platform single-instance lock: a second
	// copy fails to bind and exits silently, so the bootstrap hook can spawn
	// the app unconditionally.
	singleInstanceAddr = "127.0.0.1:47816"

	// Sessions that crashed without a SessionEnd hook leave files behind;
	// ignore anything untouched for this long.
	staleAfter = 12 * time.Hour

	pollInterval = time.Second
)

var stateDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "semaphore")
}()

type status struct {
	state    string // "red" | "orange" | "green" | "idle"
	sessions int
}

var labels = map[string]string{
	"red":    "Claude needs your input",
	"orange": "Claude is working…",
	"green":  "Task finished",
	"idle":   "No active Claude sessions",
}

func main() {
	lock, err := net.Listen("tcp", singleInstanceAddr)
	if err != nil {
		os.Exit(0) // another instance is already running
	}
	defer lock.Close()

	_ = os.MkdirAll(stateDir, 0o755)
	systray.Run(onReady, func() {})
}

func onReady() {
	statusItem := systray.AddMenuItem(labels["idle"], "")
	statusItem.Disable()
	systray.AddSeparator()
	resetItem := systray.AddMenuItem("Reset to idle", "Clear all session states")
	quitItem := systray.AddMenuItem("Quit", "")

	apply := func(st status) {
		systray.SetIcon(iconFor(st.state))
		label := labels[st.state]
		if st.sessions > 1 {
			label = fmt.Sprintf("%s  (%d sessions)", label, st.sessions)
		}
		systray.SetTooltip(label)
		statusItem.SetTitle(label)
	}
	apply(readStatus())

	go func() {
		tick := time.NewTicker(pollInterval)
		defer tick.Stop()
		last := ""
		for {
			select {
			case <-tick.C:
				st := readStatus()
				key := fmt.Sprintf("%s|%d", st.state, st.sessions)
				if key != last {
					last = key
					apply(st)
				}
			case <-resetItem.ClickedCh:
				entries, _ := os.ReadDir(stateDir)
				for _, e := range entries {
					_ = os.Remove(filepath.Join(stateDir, e.Name()))
				}
				last = ""
			case <-quitItem.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

func readStatus() status {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return status{state: "idle"}
	}
	cutoff := time.Now().Add(-staleAfter)
	anyRed := false
	newest := time.Time{}
	newestState := ""
	sessions := 0

	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().Before(cutoff) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(stateDir, e.Name()))
		if err != nil {
			continue
		}
		state := strings.TrimSpace(string(raw))
		if state != "red" && state != "orange" && state != "green" {
			continue
		}
		sessions++
		if state == "red" {
			anyRed = true
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
			newestState = state
		}
	}

	switch {
	case anyRed:
		return status{"red", sessions}
	case newestState == "green":
		return status{"green", sessions}
	case sessions > 0:
		return status{"orange", sessions}
	default:
		return status{"idle", 0}
	}
}

// --- icons ---------------------------------------------------------------

var iconCache = map[string][]byte{}

func iconFor(state string) []byte {
	if b, ok := iconCache[state]; ok {
		return b
	}
	colors := map[string]color.RGBA{
		"red":    {R: 0xE5, G: 0x39, B: 0x35, A: 0xFF},
		"orange": {R: 0xFF, G: 0x98, B: 0x00, A: 0xFF},
		"green":  {R: 0x43, G: 0xA0, B: 0x47, A: 0xFF},
		"idle":   {R: 0x9E, G: 0x9E, B: 0x9E, A: 0xFF},
	}
	size := 22
	if runtime.GOOS == "windows" {
		size = 32
	}
	b := circlePNG(colors[state], size)
	if runtime.GOOS == "windows" {
		b = pngToICO(b, size)
	}
	iconCache[state] = b
	return b
}

// circlePNG renders a filled, edge-antialiased circle.
func circlePNG(c color.RGBA, size int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cx := float64(size) / 2
	cy := float64(size) / 2
	r := float64(size)/2 - 1
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := math.Sqrt(dx*dx + dy*dy)
			if d > r {
				continue
			}
			a := 1.0
			if d > r-1 {
				a = r - d
			}
			img.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, uint8(a * float64(c.A))})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// pngToICO wraps PNG data in a minimal single-image ICO container
// (PNG-in-ICO is supported since Windows Vista).
func pngToICO(pngBytes []byte, size int) []byte {
	buf := new(bytes.Buffer)
	_ = binary.Write(buf, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(buf, binary.LittleEndian, uint16(1)) // type: icon
	_ = binary.Write(buf, binary.LittleEndian, uint16(1)) // image count
	buf.WriteByte(byte(size))                             // width
	buf.WriteByte(byte(size))                             // height
	buf.WriteByte(0)                                      // palette colors
	buf.WriteByte(0)                                      // reserved
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))               // planes
	_ = binary.Write(buf, binary.LittleEndian, uint16(32))              // bpp
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(pngBytes)))   // data size
	_ = binary.Write(buf, binary.LittleEndian, uint32(6+16))            // data offset
	buf.Write(pngBytes)
	return buf.Bytes()
}
