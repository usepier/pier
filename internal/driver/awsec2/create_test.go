package awsec2

import (
	"testing"
	"time"

	"github.com/kerem-kaynak/pier/internal/driver"
)

// AGE reads the created tag: launch time resets on every resume and the
// beacon's state changes are even noisier, so create time must ride a tag.
func TestTagListCarriesCreateTime(t *testing.T) {
	spec := driver.CreateSpec{Name: "x", Repo: "/tmp/myrepo", Branch: "main"}
	for _, tag := range tagList(spec, "me", "pier-x") {
		if tag["Key"] != TagCreated {
			continue
		}
		if _, err := time.Parse(time.RFC3339, tag["Value"]); err != nil {
			t.Fatalf("%s value %q is not RFC3339: %v", TagCreated, tag["Value"], err)
		}
		return
	}
	t.Fatalf("tagList missing %s", TagCreated)
}

// The pool tag is what separates a warm member from a user session in every
// view — a regular create must never carry it, a pool create always must.
func TestTagListPoolGen(t *testing.T) {
	spec := driver.CreateSpec{Name: "x", Repo: "/tmp/myrepo", Branch: "main"}
	for _, tag := range tagList(spec, "me", "pier-x") {
		if tag["Key"] == TagPool {
			t.Fatalf("regular create carries %s=%q", TagPool, tag["Value"])
		}
	}
	spec.PoolGen = "ab12cd34ef56"
	for _, tag := range tagList(spec, "me", "pier-x") {
		if tag["Key"] == TagPool {
			if tag["Value"] != spec.PoolGen {
				t.Fatalf("%s = %q, want %q", TagPool, tag["Value"], spec.PoolGen)
			}
			return
		}
	}
	t.Fatalf("pool create missing %s", TagPool)
}
