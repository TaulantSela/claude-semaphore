package main

// Tray icons. The working (orange) and needs-input (red) states are ANIMATED:
//
//	orange — breathes (opacity) to signal "something is happening", but only
//	         while a session is actually busy; a walked-away session holds a
//	         steady bright dot instead of breathing all day.
//	red    — beats like a heart (a two-beat pulse with a soft glow bloom) to
//	         draw the eye without alarm, and keeps beating until you respond
//	         and the state clears.
//
// Both animations cycle a small, FIXED set of PNG frames pre-rendered once at
// startup — never frames derived from a live clock — because systray's Windows
// and Linux backends cache one icon handle per distinct payload and never free
// it, so an open-ended set of frames would leak handles in a process that runs
// for days.
//
// Every frame costs a PNG decode plus a main-thread round trip (and, on macOS
// 26, an XPC hop to Control Center), so each state animates only as fast as it
// needs to: the heartbeat's short beats need ~20 fps to read as motion, while
// the slow, shallow orange breathe looks the same at ~7 fps for a third of the
// cost — and orange is the state the tray sits in most of the working day.

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// animInterval is the frame period per animated state.
var animInterval = map[string]time.Duration{
	stRed:    50 * time.Millisecond,
	stOrange: 150 * time.Millisecond,
}

// Non-premultiplied colours, tuned vibrant so they read clearly at menu-bar
// size. circlePNG renders into an NRGBA image, so these stay true at any alpha
// — a premultiplied RGBA image would turn a half-transparent orange into dark
// green. Idle stays a muted grey on purpose; it should not draw the eye.
var stateColor = map[string]color.NRGBA{
	stRed:    {R: 0xFF, G: 0x10, B: 0x10, A: 0xFF}, // near-pure vivid red
	stOrange: {R: 0xFF, G: 0x8A, B: 0x00, A: 0xFF}, // hot vivid orange
	stGreen:  {R: 0x0A, G: 0xE2, B: 0x54, A: 0xFF}, // hot vivid green
	stIdle:   {R: 0x9E, G: 0x9E, B: 0x9E, A: 0xFF},
}

// frame describes one rendered still. All values are fractions so the same
// table renders at any icon size.
type frame struct {
	radiusFrac float64 // 1.0 = fills the icon (minus a 1px margin)
	alpha      float64 // 1.0 = opaque
	liftFrac   float64 // fraction of icon height to shift the disc upward
	flash      float64 // 0 = state colour, 1 = white (brightness flash)
	glow       float64 // 0 = none; a soft halo blooming beyond the disc rim
}

// frameIcons holds the encoded, ready-to-hand-to-systray bytes for every
// state — one entry for static states, a full sequence for animated ones.
// Built once in buildFrames() and read-only thereafter.
var frameIcons = map[string][][]byte{}

func buildFrames() {
	size := iconSize()
	reduce := motionReduced()
	win := runtime.GOOS == "windows"
	tables := map[string][]frame{
		stIdle:   {{radiusFrac: 1.0, alpha: 1.0}},
		stGreen:  {{radiusFrac: 1.0, alpha: 1.0}},
		stOrange: orangeFrames(reduce),
		stRed:    redFrames(reduce),
	}
	for state, table := range tables {
		col := stateColor[state]
		icons := make([][]byte, len(table))
		for i, f := range table {
			b := circlePNG(col, size, f.radiusFrac, f.alpha, f.liftFrac, f.flash, f.glow)
			if win {
				b = pngToICO(b, size)
			}
			icons[i] = b
		}
		frameIcons[state] = icons
	}
	frameIcons[stNone] = frameIcons[stIdle]
}

// framesFor returns how many frames span a duration at a state's tick rate, so
// a cycle's wall-clock period stays fixed if the tick rate changes.
func framesFor(d time.Duration, state string) int {
	n := int(d / animInterval[state])
	if n < 2 {
		n = 2
	}
	return n
}

// orangeFrames is a calm opacity breathe on a size-steady disc. Breathing the
// radius instead would read as edge shimmer at menu-bar size; dimming does not.
// Frame 0 is fully bright so entering "working" — and the static stale-session
// dot — never begins mid-fade. One breath ≈ 1.4 s.
func orangeFrames(reduce bool) []frame {
	if reduce {
		return []frame{{radiusFrac: 1.0, alpha: 1.0}}
	}
	n := framesFor(1400*time.Millisecond, stOrange)
	fs := make([]frame, n)
	for i := 0; i < n; i++ {
		bright := 0.5 + 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)) // 1→0→1
		fs[i] = frame{radiusFrac: 1.0, alpha: 0.85 + 0.15*bright}     // 1.0→0.85→1.0
	}
	return fs
}

// redFrames is a calm two-beat heartbeat: a quick "lub", a softer "dub", then
// a rest, looping ~1.2 s per beat. The dot stays a solid red throughout and
// only swells gently on each beat (with a faint warm lift, never toward white),
// so it draws the eye through rhythm rather than a brightness assault — present
// and alive without reading as an alarm. It loops until you respond.
func redFrames(reduce bool) []frame {
	if reduce {
		return []frame{{radiusFrac: 1.0, alpha: 1.0}}
	}
	// bump is a smooth 0→1→0 pulse of the given width centred on center.
	bump := func(ph, center, width float64) float64 {
		d := ph - center
		if d < -width/2 || d > width/2 {
			return 0
		}
		return 0.5 + 0.5*math.Cos(2*math.Pi*d/width)
	}
	n := framesFor(1200*time.Millisecond, stRed) // one heartbeat ≈ 1.2 s
	fs := make([]frame, n)
	for i := 0; i < n; i++ {
		ph := float64(i) / float64(n)
		// Two beats close together (lub louder than dub), then a long rest.
		beat := math.Max(bump(ph, 0.09, 0.13), 0.62*bump(ph, 0.25, 0.13))
		fs[i] = frame{
			radiusFrac: 0.86 + 0.06*beat, // a big dot; swells a little on each beat
			alpha:      1.0,
			flash:      0.30 * beat,      // a warm lift on each beat, never white
			glow:       0.55 + 1.05*beat, // steady halo, blooms bright on each beat
		}
	}
	return fs
}

// iconSize is the source bitmap size in px. macOS downscales to 16pt, so a
// larger source stays crisp on Retina; Windows wants 32; others 22.
func iconSize() int {
	switch runtime.GOOS {
	case "windows":
		return 32
	case "darwin":
		return 44
	default:
		return 22
	}
}

// motionReduced reports the macOS "Reduce Motion" accessibility setting.
// Elsewhere it is always false. Read once at startup; a change needs a
// restart, which is acceptable for an accessibility preference.
func motionReduced() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.Command("defaults", "read", "com.apple.universalaccess", "reduceMotion").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// circlePNG renders a filled, edge-antialiased circle. radiusFrac scales the
// disc, alpha scales its opacity, liftFrac shifts it upward, flash lightens the
// colour toward white (0 = state colour, 1 = white), and glow adds a soft halo
// that blooms beyond the rim (0 = none). It draws into an NRGBA image so the
// colour stays true at partial alpha.
func circlePNG(c color.NRGBA, size int, radiusFrac, alpha, liftFrac, flash, glow float64) []byte {
	lerp := func(v uint8) uint8 { return uint8(float64(v) + (255-float64(v))*flash) }
	cr, cg, cb := lerp(c.R), lerp(c.G), lerp(c.B)
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx := float64(size) / 2
	cy := float64(size)/2 - liftFrac*float64(size)
	r := (float64(size)/2 - 1) * radiusFrac
	gw := glow * float64(size) * 0.14 // how far the halo reaches past the rim
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d := math.Sqrt(dx*dx + dy*dy)
			var cov float64
			switch {
			case d <= r:
				cov = 1.0
				if d > r-1 {
					cov = r - d // antialias the last pixel of the rim
				}
			case glow > 0 && d < r+gw:
				f := 1 - (d-r)/gw         // 1 at the rim → 0 at the halo's edge
				cov = glow * 0.90 * f * f // soft quadratic falloff
			default:
				continue
			}
			if cov > 1 {
				cov = 1 // a strong glow can exceed 1; clamp before it overflows
			}
			a := uint8(cov * alpha * float64(c.A))
			img.SetNRGBA(x, y, color.NRGBA{R: cr, G: cg, B: cb, A: a})
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
	_ = binary.Write(buf, binary.LittleEndian, uint16(0))             // reserved
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))             // type: icon
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))             // image count
	buf.WriteByte(byte(size))                                         // width
	buf.WriteByte(byte(size))                                         // height
	buf.WriteByte(0)                                                  // palette colors
	buf.WriteByte(0)                                                  // reserved
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))             // planes
	_ = binary.Write(buf, binary.LittleEndian, uint16(32))            // bpp
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(pngBytes))) // data size
	_ = binary.Write(buf, binary.LittleEndian, uint32(6+16))          // data offset
	buf.Write(pngBytes)
	return buf.Bytes()
}
