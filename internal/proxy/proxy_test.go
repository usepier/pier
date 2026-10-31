package proxy

import "testing"

func TestCtlNameIsShort(t *testing.T) {
	long := "pier-an-instance-name-that-uses-every-character-gce-allows-123456"
	if n := len(ctlName(long)); n > 20 {
		t.Errorf("control socket name is %d bytes", n)
	}
	if ctlName("a") == ctlName("b") {
		t.Error("different sessions need different sockets")
	}
}
