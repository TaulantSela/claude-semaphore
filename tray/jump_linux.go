package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func procInfo(pid int) (int, string, bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	raw, err := os.ReadFile(dir + "/stat")
	if err != nil {
		return 0, "", false
	}
	// "pid (comm) state ppid …": comm may hold spaces and parentheses, so
	// count fields from its last ')'.
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, "", false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, "", false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, "", false
	}
	exe, _ := os.Readlink(dir + "/exe")
	return ppid, strings.TrimSuffix(exe, " (deleted)"), true
}

func openFiles(pid int) []string {
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if p, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil && strings.HasPrefix(p, "/") {
			files = append(files, p)
		}
	}
	return files
}

// Terminal tabs are only scripted on macOS.
func ttyOf(int) string { return "" }

func jump(t target) error {
	if t.url == "" {
		return nil
	}
	_, err := run(10*time.Second, "xdg-open", t.url)
	return err
}

func openURL(u string) error {
	_, err := run(10*time.Second, "xdg-open", u)
	return err
}
