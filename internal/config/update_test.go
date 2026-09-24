package config

import (
	"os"
	"sync"
	"testing"
)

// The bug this exists to prevent: pier is many short-lived processes plus a
// long-lived TUI, all rewriting one file whole. A process that read the config
// a minute ago used to write back what it read, erasing anything recorded
// since. The baked-image map was the casualty that hurt, because losing it is
// silent — the next create launches from the stock image and the repo's setup
// script fails minutes later on a toolchain that should have been baked in.
func TestUpdateDoesNotLoseAConcurrentBake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := Default()
	base.AWS.Region = "eu-central-1"
	if err := base.Save(); err != nil {
		t.Fatal(err)
	}

	// Terminal A opens the TUI settings page: it reads the config now.
	stale, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	// Terminal B finishes `pier bake` and records the image.
	if _, err := Update(func(c *Config) error {
		c.RecordBake("flb-estimation", "ami-01ccf62352e7036a2")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Terminal A now saves an unrelated setting. It must not revert the bake.
	if _, err := Update(func(c *Config) error { return Set(c, "idle_timeout", "1h") }); err != nil {
		t.Fatal(err)
	}
	_ = stale // the page's own copy is deliberately not what gets written

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if img := got.BakedImage("flb-estimation"); img != "ami-01ccf62352e7036a2" {
		t.Errorf("an unrelated settings save reverted the bake: BakedImage = %q", img)
	}
	if got.IdleTimeout != "1h" {
		t.Errorf("the setting must still land, got %q", got.IdleTimeout)
	}
}

// Two repos baking at once must both survive — the second to finish used to
// write back a config read before the first one recorded anything.
func TestConcurrentUpdatesAllLand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Default().Save(); err != nil {
		t.Fatal(err)
	}

	repos := []string{"flb-estimation", "pier", "pier-website", "kuro-app"}
	var wg sync.WaitGroup
	errs := make([]error, len(repos))
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = Update(func(c *Config) error {
				c.RecordBake(repo, "ami-"+repo)
				return nil
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", repos[i], err)
		}
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range repos {
		if img := got.BakedImage(repo); img != "ami-"+repo {
			t.Errorf("%s was lost by a concurrent write: got %q", repo, img)
		}
	}
}

// A config that won't parse must never be silently replaced with defaults —
// that would drop the baked images, the manifest and the token in one write,
// which is precisely the failure this package is hardening against.
func TestUpdateRefusesToOverwriteAnUnparseableConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Default().Save(); err != nil {
		t.Fatal(err)
	}
	damaged := "driver = \"aws-ec2\"\n[aws\n"
	if err := os.WriteFile(Path(), []byte(damaged), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(func(c *Config) error { return Set(c, "idle_timeout", "1h") }); err == nil {
		t.Fatal("want a refusal, not a reset to defaults")
	}
	// The damaged file is left alone for the user to fix or remove.
	b, err := os.ReadFile(Path())
	if err != nil || string(b) != damaged {
		t.Errorf("the unparseable config must be left untouched, got %q (%v)", b, err)
	}
}
