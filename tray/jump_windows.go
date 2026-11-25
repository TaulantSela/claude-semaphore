package main

import (
	"syscall"
	"time"
	"unsafe"
)

func procInfo(pid int) (int, string, bool) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", false
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		if int(e.ProcessID) == pid {
			return int(e.ParentProcessID), exePath(pid), true
		}
	}
	return 0, "", false
}

var queryFullProcessImageName = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

func exePath(pid int) string {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	r, _, _ := queryFullProcessImageName.Call(uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

// Windows offers no simple way to list another process's open files, so
// windowOf falls back to the editor's renderer logs.
func openFiles(int) []string { return nil }

func ttyOf(int) string { return "" }

func jump(t target) error {
	if t.url == "" {
		return nil
	}
	_, err := run(10*time.Second, "rundll32", "url.dll,FileProtocolHandler", t.url)
	return err
}

func openURL(u string) error {
	_, err := run(10*time.Second, "rundll32", "url.dll,FileProtocolHandler", u)
	return err
}
