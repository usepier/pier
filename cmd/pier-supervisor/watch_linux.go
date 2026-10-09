package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// maxWatches bounds the directories one checkout's watch takes; past it,
// the tree is polled instead.
const maxWatches = 20000

const watchMask = syscall.IN_CLOSE_WRITE | syscall.IN_CREATE | syscall.IN_DELETE |
	syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO | syscall.IN_DELETE_SELF | syscall.IN_ATTRIB

// watchTree calls changed when anything in the checkout git cares about
// changes: its files (less what it ignores) and the refs, index and HEAD
// in .git. It returns a stop function.
func watchTree(dir string, changed func()) (stop func()) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return pollTree(dir, changed)
	}
	w := &treeWatch{fd: fd, dir: dir, dirs: map[int]string{}, ignored: ignoredDirs(dir)}
	if !w.add(dir) || !w.addGit() {
		syscall.Close(fd)
		return pollTree(dir, changed)
	}
	// Nonblocking, the descriptor goes through Go's poller: closing the
	// file ends a read waiting on it.
	f := os.NewFile(uintptr(fd), "inotify")
	go w.run(f, changed)
	var once sync.Once
	return func() { once.Do(func() { f.Close() }) }
}

type treeWatch struct {
	fd      int
	dir     string
	dirs    map[int]string // watch descriptor → directory
	ignored map[string]bool
	full    bool
}

// add watches a directory and everything under it git doesn't ignore;
// false once the watch list is full.
func (w *treeWatch) add(root string) bool {
	ok := true
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(w.dir, p)
		if rel == ".git" || w.ignored[filepath.ToSlash(rel)] {
			return filepath.SkipDir
		}
		if len(w.dirs) >= maxWatches {
			ok = false
			return filepath.SkipAll
		}
		wd, err := syscall.InotifyAddWatch(w.fd, p, watchMask)
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		w.dirs[wd] = p
		return nil
	})
	return ok
}

// addGit watches what moves in .git when the checkout's state does: HEAD
// and the index (in .git itself) and the refs.
func (w *treeWatch) addGit() bool {
	g := filepath.Join(w.dir, ".git")
	wd, err := syscall.InotifyAddWatch(w.fd, g, watchMask)
	if err != nil {
		return false
	}
	w.dirs[wd] = g
	ok := true
	filepath.WalkDir(filepath.Join(g, "refs"), func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		wd, err := syscall.InotifyAddWatch(w.fd, p, watchMask)
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		w.dirs[wd] = p
		return nil
	})
	return ok
}

func (w *treeWatch) run(f *os.File, changed func()) {
	buf := make([]byte, 64<<10)
	for {
		n, err := f.Read(buf)
		if err != nil {
			return
		}
		fire := false
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameBytes := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
			name := strings.TrimRight(string(nameBytes), "\x00")
			off += syscall.SizeofInotifyEvent + int(ev.Len)
			parent := w.dirs[int(ev.Wd)]
			if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
				fire = true
				continue
			}
			if ev.Mask&syscall.IN_IGNORED != 0 {
				delete(w.dirs, int(ev.Wd))
				continue
			}
			if parent == "" {
				continue
			}
			if w.noise(parent, name) {
				continue
			}
			fire = true
			p := filepath.Join(parent, name)
			if ev.Mask&syscall.IN_ISDIR != 0 && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 && !w.full {
				if w.inGit(parent) || !gitIgnores(w.dir, p) {
					if !w.add(p) {
						// Too many to watch: the safety re-read covers
						// the rest.
						w.full = true
					}
				}
			}
		}
		if fire {
			changed()
		}
	}
}

func (w *treeWatch) inGit(p string) bool {
	return strings.HasPrefix(p, filepath.Join(w.dir, ".git"))
}

// noise is a change in .git that isn't the checkout's state moving: lock
// files, objects being written, logs.
func (w *treeWatch) noise(parent, name string) bool {
	if !w.inGit(parent) {
		return false
	}
	if strings.HasSuffix(name, ".lock") {
		return true
	}
	if parent == filepath.Join(w.dir, ".git") {
		switch name {
		case "HEAD", "index", "packed-refs", "ORIG_HEAD", "MERGE_HEAD", "REBASE_HEAD", "CHERRY_PICK_HEAD", "FETCH_HEAD", "config", "refs":
			return false
		}
		return true
	}
	return false
}

// ignoredDirs are the directories git ignores in a checkout
// (node_modules, build output): watching them would cost much and show
// nothing.
func ignoredDirs(dir string) map[string]bool {
	out, _ := exec.Command("git", "-C", dir, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory").Output()
	m := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if strings.HasSuffix(p, "/") {
			m[strings.TrimSuffix(p, "/")] = true
		}
	}
	return m
}

func gitIgnores(dir, p string) bool {
	return exec.Command("git", "-C", dir, "check-ignore", "-q", p).Run() == nil
}
