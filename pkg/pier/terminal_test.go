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

func TestParseEnv(t *testing.T) {
	e, err := ParseEnv([]byte(`{"windows":"@1|0|1|claude|agent|agent\n","containers":"{\"ID\":\"c1\",\"Names\":\"shop-db\",\"Image\":\"postgres:16\",\"State\":\"running\",\"Status\":\"Up 2 hours\",\"Ports\":\"0.0.0.0:5432->5432/tcp\"}\n","processes":[{"pid":7,"command":"node server.js","cpu":40.5,"rss":2048}],"ports":[{"port":5432,"process":"docker-proxy","pid":9}],"res":{"cpu":12,"mem_used":1,"mem_total":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Windows) != 1 || len(e.Containers) != 1 || e.Containers[0].Name != "shop-db" || e.Processes[0].CPU != 40.5 || e.Ports[0].Port != 5432 || e.Res.CPU != 12 {
		t.Errorf("env = %+v", e)
	}
}
