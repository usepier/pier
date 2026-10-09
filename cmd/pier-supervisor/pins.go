package main

// The agent programs chats run, pinned: the versions this pier was built
// and tested against, downloaded once per session (or image) and checked
// against their published checksums. Programs the user runs in a terminal
// are whatever they installed; chats never depend on those updating
// themselves into a protocol this supervisor doesn't speak.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type pin struct {
	Name    string // the program: claude, codex, rg
	Version string
	// URL and SHA256 by GOARCH; Member names the program inside a .tar.gz
	// download ("" for a bare binary).
	URL    map[string]string
	SHA256 map[string]string
	Member map[string]string
}

var pins = map[string]pin{
	"claude": {
		Name: "claude", Version: "2.1.286",
		URL: map[string]string{
			"arm64": "https://downloads.claude.ai/claude-code-releases/2.1.286/linux-arm64/claude",
			"amd64": "https://downloads.claude.ai/claude-code-releases/2.1.286/linux-x64/claude",
		},
		SHA256: map[string]string{
			"arm64": "0292fa22ac2fd43e16be9d0e511ddd8347280d6e0ebaca744ef5b27e05d8d0f8",
			"amd64": "fe503f65c6289d59c23e5b21ae44f03583f997dd33a2cbfc75ab4f96fb8fc73f",
		},
	},
	"codex": {
		Name: "codex", Version: "0.159.3",
		URL: map[string]string{
			"arm64": "https://github.com/openai/codex/releases/download/rust-v0.159.3/codex-aarch64-unknown-linux-musl.tar.gz",
			"amd64": "https://github.com/openai/codex/releases/download/rust-v0.159.3/codex-x86_64-unknown-linux-musl.tar.gz",
		},
		SHA256: map[string]string{
			"arm64": "cd5f307b3fcd6080773e684b86c3114a67d4f1c61dc447be09876b552eb4bea7",
			"amd64": "b48ca1b2d6b1bf42b944e02c3d937c898e24651916684cdc35fdedf31b291bcb",
		},
		Member: map[string]string{
			"arm64": "codex-aarch64-unknown-linux-musl",
			"amd64": "codex-x86_64-unknown-linux-musl",
		},
	},
	"rg": {
		Name: "rg", Version: "15.1.0",
		URL: map[string]string{
			"arm64": "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-aarch64-unknown-linux-gnu.tar.gz",
			"amd64": "https://github.com/BurntSushi/ripgrep/releases/download/15.1.0/ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz",
		},
		SHA256: map[string]string{
			"arm64": "2b661c6ef508e902f388e9098d9c4c5aca72c87b55922d94abdba830b4dc885e",
			"amd64": "1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599",
		},
		Member: map[string]string{
			"arm64": "ripgrep-15.1.0-aarch64-unknown-linux-gnu/rg",
			"amd64": "ripgrep-15.1.0-x86_64-unknown-linux-musl/rg",
		},
	},
}

// pinDir is where a pinned program lives.
func pinDir(home string, p pin) string {
	return filepath.Join(home, ".local", "share", "pier", "agents", p.Name, p.Version)
}

// pinned installs pinned programs, one download at a time per program.
type pinned struct {
	home string
	mu   sync.Mutex
	busy map[string]*sync.Mutex
}

func newPinned(home string) *pinned { return &pinned{home: home, busy: map[string]*sync.Mutex{}} }

func (p *pinned) lock(name string) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.busy[name]
	if m == nil {
		m = &sync.Mutex{}
		p.busy[name] = m
	}
	return m
}

// path is the pinned program, installing it first if it isn't yet.
func (p *pinned) path(name string) (string, error) {
	pn, ok := pins[name]
	if !ok {
		return "", fmt.Errorf("no pinned %s", name)
	}
	bin := filepath.Join(pinDir(p.home, pn), pn.Name)
	if fi, err := os.Stat(bin); err == nil && fi.Mode()&0o111 != 0 {
		return bin, nil
	}
	m := p.lock(name)
	m.Lock()
	defer m.Unlock()
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if err := install(pn, bin); err != nil {
		return "", fmt.Errorf("installing %s %s: %w", pn.Name, pn.Version, err)
	}
	return bin, nil
}

// program is the program a chat runs: the pinned one, or — when it can't
// be had — whatever is on PATH, with a note why.
func (p *pinned) program(name string) (path, note string) {
	bin, err := p.path(name)
	if err == nil {
		return bin, ""
	}
	if found, lerr := exec.LookPath(name); lerr == nil {
		return found, err.Error() + "; using the installed " + name
	}
	return "", err.Error()
}

// installAll fetches every pinned program in the background, so the first
// chat doesn't wait for a download.
func (p *pinned) installAll() {
	for name := range pins {
		if _, err := p.path(name); err != nil {
			fmt.Println("pier-supervisor:", err)
		}
	}
}

var pinClient = &http.Client{Timeout: 10 * time.Minute}

func install(pn pin, bin string) error {
	arch := runtime.GOARCH
	url, sum := pn.URL[arch], pn.SHA256[arch]
	if url == "" {
		return fmt.Errorf("no build for %s", arch)
	}
	dir := filepath.Dir(bin)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	resp, err := pinClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("%s: checksum %s, want %s", url, got, sum)
	}
	out := bin + ".new"
	if member := pn.Member[arch]; member != "" {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := untarMember(tmp, member, out); err != nil {
			return err
		}
	} else {
		tmp.Close()
		if err := os.Rename(tmp.Name(), out); err != nil {
			return err
		}
	}
	if err := os.Chmod(out, 0o755); err != nil {
		return err
	}
	return os.Rename(out, bin)
}

func untarMember(r io.Reader, member, out string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s isn't in the download", member)
		}
		if err != nil {
			return err
		}
		if strings.TrimPrefix(hdr.Name, "./") != member {
			continue
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
}
