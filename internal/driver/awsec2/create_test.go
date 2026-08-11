package awsec2

import (
	"testing"
	"time"

	"github.com/usepier/pier/internal/driver"
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
