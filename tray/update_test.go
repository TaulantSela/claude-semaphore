package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.3.3", "0.3.2", true},
		{"0.10.0", "0.9.9", true},
		{"1.0", "0.9.9", true},
		{"0.3.2", "0.3.2", false},
		{"0.3.1", "0.3.2", false},
		{"0.3.2.1", "0.3.2", true},
		{"0.3.2", "0.3.2.0", false},
		{"0.3.3", "dev", false},
		{"", "0.3.2", false},
		{"v0.3.3", "0.3.2", false},
		{"0.3.x", "0.3.2", false},
	} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAvailableUpdate(t *testing.T) {
	now := time.Now()
	write := func(t *testing.T, body string, age time.Duration) string {
		f := filepath.Join(t.TempDir(), ".latest")
		writeFile(t, f, body)
		if err := os.Chtimes(f, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		return f
	}
	day := 25 * time.Hour
	for _, c := range []struct {
		name, body, have string
		age              time.Duration
		want             string
	}{
		{"newer, known for a day", "0.3.3\n", "0.3.2", day, "0.3.3"},
		{"newer, just seen", "0.3.3", "0.3.2", time.Hour, ""},
		{"same version", "0.3.2", "0.3.2", day, ""},
		{"older release", "0.3.1", "0.3.2", day, ""},
		{"dev build", "0.3.3", "dev", day, ""},
		{"garbage", "<html>", "0.3.2", day, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := availableUpdate(write(t, c.body, c.age), c.have, now); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
	if got := availableUpdate(filepath.Join(t.TempDir(), "missing"), "0.3.2", now); got != "" {
		t.Errorf("missing file: got %q", got)
	}
	if got := availableUpdate("", "0.3.2", now); got != "" {
		t.Errorf("no path: got %q", got)
	}
}
