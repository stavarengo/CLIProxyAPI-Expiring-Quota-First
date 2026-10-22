package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestShortDurationSpellsDefaultsLikeConfig(t *testing.T) {
	for d, want := range map[time.Duration]string{
		168 * time.Hour:  "168h",
		15 * time.Minute: "15m",
		10 * time.Minute: "10m",
		30 * time.Second: "30s",
		90 * time.Second: "1m30s",
		0:                "0s",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// configureWith sends the plugin's config block the way CLIProxyAPI does, as base64 YAML.
func configureWith(t *testing.T, yaml string) {
	t.Helper()
	raw := []byte(`{"config_yaml":"` + base64.StdEncoding.EncodeToString([]byte(yaml)) + `"}`)
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
}

func TestReconfigureKeepsWhatWasLearnedWithoutTheStateFile(t *testing.T) {
	dir := t.TempDir()
	configureWith(t, "mode: shadow\nstate_file: "+filepath.Join(dir, "old.json"))
	t.Cleanup(stop)
	r := active()
	claudeReset(r, "a", time.Now().Add(time.Hour))
	r.pick(pickReq("s1", "a"))
	// Nothing has been saved to the new path; what was learned must still be in memory, and the mode switch alone drops the shadow bindings.
	configureWith(t, "mode: active\nstate_file: "+filepath.Join(dir, "new.json"))
	if active() != r {
		t.Fatal("reconfigure swapped the router instead of changing it in place")
	}
	if _, ok := r.st.Accounts["a"]; !ok {
		t.Fatal("reconfigure lost the account learned just before it")
	}
	if len(r.st.Bindings) != 0 || r.st.Mode != "active" {
		t.Fatalf("mode switch kept shadow bindings: %v in %s", r.st.Bindings, r.st.Mode)
	}
	if err := r.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.json")); err != nil {
		t.Fatalf("the next save did not go to the new state file: %v", err)
	}
}

func TestQuiesceSavesForTheReplacementCopy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	configureWith(t, "mode: shadow\nstate_file: "+file)
	t.Cleanup(stop)
	claudeReset(active(), "a", time.Now().Add(time.Hour))
	out, err := handle(pluginabi.MethodPluginQuiesce, nil)
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("quiesce answered %s, %v", out, err)
	}
	// No tick has run; only the final save that quiesce triggers can carry a to the copy that loads next.
	if _, ok := newRouter(active().cfg).st.Accounts["a"]; !ok {
		t.Fatal("the state file does not hold what the plugin learned before quiesce")
	}
	if active() == nil {
		t.Fatal("quiesce stopped the plugin answering before it was retired")
	}
}
