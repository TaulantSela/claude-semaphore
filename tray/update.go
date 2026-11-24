package main

// The update notice. bootstrap.sh, which already downloads the tray from
// GitHub, looks up the latest release at most once a day and writes its
// version to .latest beside the tray binary, rewriting the file only when the
// version changes. The tray itself never touches the network: it reads that
// file, and once a version newer than its own has been known for a day it
// adds a menu row that opens the update instructions. The day's grace keeps
// the row from people whose plugin updates itself.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// version is the tray's own version; release builds set it with
// -ldflags "-X main.version=<version>". A dev build shows no update notice.
var version = "dev"

const (
	updateGrace = 24 * time.Hour
	updateDocs  = "https://github.com/TaulantSela/claude-semaphore#install"
)

// latestFile is where bootstrap.sh records the latest release's version.
func latestFile() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), ".latest")
}

// availableUpdate returns the version named in file when it is newer than
// have and has been known for at least updateGrace, or "".
func availableUpdate(file, have string, now time.Time) string {
	if file == "" {
		return ""
	}
	st, err := os.Stat(file)
	if err != nil || now.Sub(st.ModTime()) < updateGrace {
		return ""
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	latest := strings.TrimSpace(string(raw))
	if !newerVersion(latest, have) {
		return ""
	}
	return latest
}

// newerVersion reports whether dotted version a is newer than b. Anything
// that is not plain dotted numbers, such as "dev", compares as not newer.
func newerVersion(a, b string) bool {
	pa, ok1 := parseVersion(a)
	pb, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func parseVersion(v string) ([]int, bool) {
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}
