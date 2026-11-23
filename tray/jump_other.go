//go:build !darwin && !linux && !windows

package main

func procInfo(int) (int, string, bool) { return 0, "", false }
func openFiles(int) []string           { return nil }
func ttyOf(int) string                 { return "" }
func jump(target) error                { return nil }
