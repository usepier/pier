package config

import (
	"testing"
	"time"
)

func TestPoolSize(t *testing.T) {
	cfg := Default()
	if n := cfg.PoolSize("shop"); n != 0 {
		t.Errorf("PoolSize on an empty config = %d, want 0 (pools are opt-in)", n)
	}
	cfg.SetPoolSize("shop", 2)
	if n := cfg.PoolSize("shop"); n != 2 {
		t.Errorf("PoolSize = %d, want 2", n)
	}
	cfg.SetPoolSize("shop", 0)
	if _, ok := cfg.Pool.Sizes["shop"]; ok {
		t.Error("SetPoolSize(0) must remove the entry, not store a dead row")
	}
}

func TestPoolMaxAge(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 14 * 24 * time.Hour, true}, // default
		{"7d", 7 * 24 * time.Hour, true},
		{"72h", 72 * time.Hour, true},
		{"0d", 0, false},
		{"-3d", 0, false},
		{"soon", 0, false},
		{"-72h", 0, false},
	} {
		cfg := Config{Pool: Pool{MaxAge: c.in}}
		got, err := cfg.PoolMaxAge()
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("PoolMaxAge(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("PoolMaxAge(%q) must be rejected", c.in)
		}
	}
}
