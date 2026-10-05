package collect

import (
	"testing"
	"time"
)

// Measured on 0.23.0: an archive collected with --estimate-compression held
// 041's output and a config block with no estimate_compression key.
func TestRunConfigRecordsEveryOptIn(t *testing.T) {
	flags := map[string]bool{}
	for name := range KnownFlags {
		flags[name] = true
	}
	c := runConfig(Options{Config: &Config{}, Flags: flags})
	for name := range KnownFlags {
		if c[name] != "true" {
			t.Errorf("config[%q] = %q, want true for a run that set %s", name, c[name], KnownFlags[name])
		}
	}
	off := runConfig(Options{Config: &Config{}})
	for name := range KnownFlags {
		if off[name] != "false" {
			t.Errorf("config[%q] = %q, want false for a run that left it off", name, off[name])
		}
	}
}

// The bound is recorded in the unit of duration_sec beside it, and only when
// one was set, so that its absence means the same thing in this version's
// manifests and in older ones.
func TestRunConfigRecordsTheMaxDurationOnlyWhenSet(t *testing.T) {
	c := runConfig(Options{Config: &Config{MaxDuration: 2 * time.Hour}})
	if c["max_duration_sec"] != "7200" {
		t.Errorf("max_duration_sec = %q, want 7200", c["max_duration_sec"])
	}
	all := map[string]bool{}
	for name := range KnownFlags {
		all[name] = true
	}
	off := runConfig(Options{Config: &Config{}, Flags: all})
	if v, ok := off["max_duration_sec"]; ok {
		t.Errorf("a run with --all and no bound records max_duration_sec = %q", v)
	}
}
