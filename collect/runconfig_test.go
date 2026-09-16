package collect

import "testing"

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
