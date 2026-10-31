package main

// A fake agent program for the chat host's tests: the test binary run
// again, replaying a real recorded stream (pkg/pier/chat/testdata/stream).
// It writes the recording's output as the agent wrote it, and where the
// recording has the host speaking (a prompt, an answer, a control request)
// it waits for the host to say something — so a test drives it the way a
// frontend drives a real agent. Requests the recording doesn't have get an
// empty answer.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	fakeEnv = "PIER_FAKE_AGENT" // <claude|codex>:<fixture>
	// fakeHeardEnv names a file the fake appends what it hears to.
	fakeHeardEnv = "PIER_FAKE_HEARD"
)

func TestMain(m *testing.M) {
	if v := os.Getenv(fakeEnv); v != "" && len(os.Args) > 1 && os.Args[1] != "-test.run" && !isTestFlag(os.Args[1]) {
		os.Exit(fakeAgent(v))
	}
	os.Exit(m.Run())
}

func isTestFlag(a string) bool { return len(a) > 6 && a[:6] == "-test." }

type fixtureLine struct {
	Dir string          `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

// fixturePath is a recording's absolute path (the fake runs elsewhere).
func fixturePath(name string) string {
	p, _ := filepath.Abs(filepath.Join("..", "..", "pkg", "pier", "chat", "testdata", "stream", name))
	return p
}

func loadFixture(path string) ([]fixtureLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []fixtureLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var l fixtureLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

type fakeMsg struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	ID        json.RawMessage `json:"id"`
	Method    string          `json:"method"`
	Response  *struct {
		RequestID string `json:"request_id"`
	} `json:"response"`
}

func fakeAgent(spec string) int {
	kind, name, _ := strings.Cut(spec, ":")
	var heard *os.File
	if p := os.Getenv(fakeHeardEnv); p != "" {
		heard, _ = os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	lines, err := loadFixture(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake agent:", err)
		return 1
	}
	in := make(chan []byte)
	go func() {
		r := bufio.NewReaderSize(os.Stdin, 1<<20)
		for {
			b, err := r.ReadBytes('\n')
			if len(b) > 1 {
				if heard != nil {
					heard.Write(b)
				}
				in <- b
			}
			if err != nil {
				close(in)
				return
			}
		}
	}()
	out := bufio.NewWriter(os.Stdout)
	write := func(b []byte) {
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
	// ids maps the recording's request ids to the host's.
	ids := map[string]string{}
	used := map[int]bool{}
	answer := func(i int, fixtureID, actual string) {
		// The recorded answer to a request goes out at once (a host waits
		// for it before saying anything else).
		for j := i + 1; j < len(lines); j++ {
			if lines[j].Dir != "out" || used[j] {
				continue
			}
			var m map[string]any
			json.Unmarshal(lines[j].Msg, &m)
			if kind == "claude" {
				resp, _ := m["response"].(map[string]any)
				if m["type"] == "control_response" && resp != nil && resp["request_id"] == fixtureID {
					resp["request_id"] = actual
					b, _ := json.Marshal(m)
					write(b)
					used[j] = true
					return
				}
				continue
			}
			if _, isMethod := m["method"]; !isMethod && fmt.Sprint(m["id"]) == fixtureID {
				var raw any
				json.Unmarshal([]byte(actual), &raw)
				m["id"] = raw
				b, _ := json.Marshal(m)
				write(b)
				used[j] = true
				return
			}
		}
	}
	unexpected := func(got fakeMsg, raw []byte) {
		if kind == "codex" && got.Method != "" && len(got.ID) > 0 {
			write([]byte(fmt.Sprintf(`{"id":%s,"result":{"data":[]}}`, got.ID)))
		}
		if kind == "claude" && got.Type == "control_request" {
			write([]byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`, got.RequestID)))
		}
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if used[i] {
			continue
		}
		if l.Dir == "out" {
			write([]byte(l.Msg))
			continue
		}
		var want fakeMsg
		json.Unmarshal(l.Msg, &want)
		for {
			var raw []byte
			select {
			case b, ok := <-in:
				if !ok {
					return 0
				}
				raw = b
			case <-time.After(20 * time.Second):
				fmt.Fprintln(os.Stderr, "fake agent: the host never said", string(l.Msg))
				return 2
			}
			var got fakeMsg
			json.Unmarshal(raw, &got)
			if kind == "claude" {
				if want.Type == "control_request" && got.Type == "control_request" {
					ids[want.RequestID] = got.RequestID
					answer(i, want.RequestID, got.RequestID)
					break
				}
				if want.Type != got.Type {
					unexpected(got, raw)
					continue
				}
				break
			}
			// codex: a request or notification must be the one recorded; an
			// answer (to the agent's own request) must be an answer.
			if got.Method != want.Method {
				unexpected(got, raw)
				continue
			}
			if want.Method != "" && len(want.ID) > 0 {
				answer(i, string(want.ID), string(got.ID))
			}
			break
		}
	}
	// The recording is over: answer whatever else comes until the host
	// lets go.
	for b := range in {
		var got fakeMsg
		json.Unmarshal(b, &got)
		unexpected(got, b)
	}
	return 0
}
