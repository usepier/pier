package config

import (
	"os"
	"testing"
)

// The config file carries state no UI can reconstruct — the baked-image map
// above all. A repo whose pointer goes missing launches from stock and fails
// its setup script minutes later, with nothing connecting the two. So a save
// must round-trip everything it didn't touch, and must leave the previous
// version recoverable.
func TestSaveRoundTripsAndKeepsABackup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first := Default()
	first.RecordBake("flb-estimation", "ami-01ccf62352e7036a2")
	first.Secrets.ClaudeOAuthToken = "tok"
	first.Secrets.Manifest = []string{".netrc"}
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}

	// The load → edit → save path every settings write takes.
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := Set(&loaded, "idle_timeout", "1h"); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.IdleTimeout != "1h" {
		t.Errorf("the edit must land, got idle_timeout %q", got.IdleTimeout)
	}
	if img := got.BakedImage("flb-estimation"); img != "ami-01ccf62352e7036a2" {
		t.Errorf("an unrelated edit must not drop the baked image, got %q", img)
	}
	if got.Secrets.ClaudeOAuthToken != "tok" || len(got.Secrets.Manifest) != 1 {
		t.Errorf("an unrelated edit must not drop secrets, got %+v", got.Secrets)
	}

	// The version being replaced stays on disk, so a bad write is survivable.
	bak, err := os.ReadFile(Path() + ".bak")
	if err != nil {
		t.Fatalf("want a backup of the previous config: %v", err)
	}
	if !contains(string(bak), "ami-01ccf62352e7036a2") {
		t.Error("the backup must hold the previous contents")
	}
}

// A save must never leave a half-written config behind: readers see the old
// version or the new one, never a truncated file that still parses.
func TestSaveIsAtomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := Default()
	c.RecordBake("repo", "ami-1")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil { // a no-op rewrite must stay clean
		t.Fatal(err)
	}
	ents, err := os.ReadDir(Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		switch e.Name() {
		case "config.toml", "config.toml.bak", "config.toml.lock":
		default:
			t.Errorf("save left a stray temp file behind: %q", e.Name())
		}
	}
	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("the config holds secrets; want 0600, got %o", perm)
	}
}

// Switching clouds back and forth must not cost anything: each cloud's baked
// images are scoped to that cloud, and the idle one's map has to survive
// untouched. Note what the switch DOES change — while the active driver is
// gcp-gce, a repo baked only on AWS reads as unbaked, so a create launches
// from stock and its .pier/bake.sh toolchains are simply absent.
func TestDriverSwitchPreservesTheOtherCloudsBakes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := Default()
	c.RecordBake("flb-estimation", "ami-01ccf62352e7036a2")
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	for _, drv := range []string{"gcp-gce", "aws-ec2", "gcp-gce", "aws-ec2"} {
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if err := Set(&cfg, "driver", drv); err != nil {
			t.Fatal(err)
		}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		got, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if got.AWS.BakedAMIs["flb-estimation"] != "ami-01ccf62352e7036a2" {
			t.Fatalf("driver=%s lost the AWS baked-image map: %v", drv, got.AWS.BakedAMIs)
		}
		// The lookup is driver-scoped even though the map survived.
		want := "ami-01ccf62352e7036a2"
		if drv == "gcp-gce" {
			want = ""
		}
		if img := got.BakedImage("flb-estimation"); img != want {
			t.Errorf("driver=%s: BakedImage = %q, want %q", drv, img, want)
		}
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
