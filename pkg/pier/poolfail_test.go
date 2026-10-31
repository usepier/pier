package pier

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A failed fill is remembered by its first line until a fill succeeds; an
// interrupted one leaves the record as it was.
func TestPoolFailureIsRemembered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	recordFill("shop", errors.New("pool member: .pier/setup.sh failed (exit 2):\nmake: *** [dev] Error 1"))
	f := LastPoolFailure("shop")
	if f == nil || f.Message != ".pier/setup.sh failed (exit 2)" || f.At.IsZero() {
		t.Fatalf("failure = %+v", f)
	}
	recordFill("shop", fmt.Errorf("filling: %w", context.Canceled))
	if LastPoolFailure("shop") == nil {
		t.Error("an interrupted fill cleared the failure")
	}
	recordFill("shop", nil)
	if f := LastPoolFailure("shop"); f != nil {
		t.Errorf("a fill that worked left %+v", f)
	}
}
