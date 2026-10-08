package pier

import "testing"

func TestParseWindows(t *testing.T) {
	out := "@1|0|0|claude|agent|agent\n@2|1|1|zsh||zsh\n@4|2|0|bash||setup-failed\ngarbage\n"
	ws := parseWindows(out)
	if len(ws) != 3 {
		t.Fatalf("windows = %+v", ws)
	}
	if ws[0].Role != "agent" || ws[0].Command != "claude" || ws[1].Active != true || ws[2].Role != "setup" {
		t.Errorf("windows = %+v", ws)
	}
	if shellQuote("it's") != `'it'\''s'` {
		t.Error("quoting")
	}
}
