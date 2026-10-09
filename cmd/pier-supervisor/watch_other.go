//go:build !linux

package main

// watchTree polls where there is no inotify (only tests run here).
func watchTree(dir string, changed func()) (stop func()) {
	return pollTree(dir, changed)
}
