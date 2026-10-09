package payload

import (
	"strings"
	"testing"
)

// A tab is its own grouped view of main, pinned to its window, gone when
// its client detaches — and nothing from the caller reaches the script
// unchecked.
func TestTabAttach(t *testing.T) {
	s, err := TabAttach("a1", "@7")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tmux new-session -d -t main -s pier-tab-a1",
		"tmux select-window -t pier-tab-a1:@7",
		// Set once attached, in the same command list: tmux 3.4 destroys a
		// session that is unattached when the option is set.
		"exec tmux attach-session -t pier-tab-a1 \\; set-option -t pier-tab-a1 destroy-unattached on",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("tab script lacks %q:\n%s", want, s)
		}
	}
	if strings.Count(s, "destroy-unattached") != 1 {
		t.Errorf("destroy-unattached is set before the attach:\n%s", s)
	}
	if s, _ := TabAttach("a1", ""); strings.Contains(s, "select-window") {
		t.Error("no window means main's current one")
	}
	for _, bad := range [][2]string{{"a1; rm -rf ~", ""}, {"a1", "@7; reboot"}, {"", ""}, {"A1", ""}} {
		if _, err := TabAttach(bad[0], bad[1]); err == nil {
			t.Errorf("TabAttach(%q, %q) must refuse", bad[0], bad[1])
		}
	}
	full := AttachScript(MainAttach)
	if !strings.HasSuffix(full, MainAttach) || !strings.Contains(full, ".pier-bootstrapped") {
		t.Error("the attach prelude guards and then hands over")
	}
}
