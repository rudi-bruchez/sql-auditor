package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noStdin is the reader every test that does not exercise --password-stdin
// passes: reading it is a bug in the code under test, not a source of an empty
// password, so it must be distinguishable from an empty stdin.
var noStdin = strings.NewReader("")

// writeDotEnv puts a .env in a temporary directory and returns its path, so a
// test can exercise the file half of the precedence without touching the
// developer's own .env — which is the file that would otherwise be read, since
// the flag defaults to the relative name ".env".
func writeDotEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// noEnv is an environment with nothing in it. Passing it rather than os.Getenv
// keeps a test from depending on whatever SQL_* variables happen to be exported
// in the shell that runs it.
func noEnv(string) string { return "" }

func TestModeChoosesBetweenTheSubcommandTheWizardAndTheUsage(t *testing.T) {
	// The two files are only ever compared by identity here: the injected
	// predicate is what decides, which is the whole reason it is injected.
	in, out := os.Stdin, os.Stdout
	tty := func(want ...*os.File) func(*os.File) bool {
		return func(f *os.File) bool {
			for _, w := range want {
				if f == w {
					return true
				}
			}
			return false
		}
	}
	noTUI := func(k string) string {
		if k == "SQL_AUDITOR_NO_TUI" {
			return "1"
		}
		return ""
	}

	cases := []struct {
		name  string
		isTTY func(*os.File) bool
		env   func(string) string
		args  []string
		want  Mode
	}{
		// An argument wins over everything, so a scripted collect behaves the
		// same whatever the terminal is — including when there is a terminal.
		{"an argument on a terminal", tty(in, out), noEnv, []string{"collect"}, ModeSubcommand},
		{"an argument with the wizard disabled", tty(in, out), noTUI, []string{"check"}, ModeSubcommand},
		{"nothing to do on a terminal", tty(in, out), noEnv, nil, ModeTUI},
		{"the wizard disabled by the environment", tty(in, out), noTUI, nil, ModeUsage},
		// `sql-auditor > run.log` must not quietly start collecting on a
		// production instance.
		{"stdout redirected", tty(in), noEnv, nil, ModeUsage},
		{"stdin redirected", tty(out), noEnv, nil, ModeUsage},
		{"neither is a terminal", tty(), noEnv, nil, ModeUsage},
	}
	for _, c := range cases {
		if got := mode(c.isTTY, in, out, c.env, c.args); got != c.want {
			t.Errorf("%s: mode = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBuildOptionsPrefersAFlagOverTheDotEnvFile(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=from-dotenv\n")
	o, code, err := buildOptions("check", []string{"--env", env, "--server", "from-flag"}, noEnv, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.Server != "from-flag" {
		t.Errorf("Server = %q, want the flag to win", o.Config.Server)
	}
}

// The order is deliberately the reverse of the twelve-factor one. A .env is the
// file the operator edited for this instance, ten seconds ago and on purpose; an
// exported SQL_SERVER is usually left over from another session, and silently
// beating the file the wizard displays would send the connection somewhere other
// than the screen says.
func TestBuildOptionsPrefersTheDotEnvFileOverAnExportedVariable(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=from-dotenv\n")
	exported := func(k string) string {
		if k == "SQL_SERVER" {
			return "from-environment"
		}
		return ""
	}
	o, code, err := buildOptions("check", []string{"--env", env}, exported, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.Server != "from-dotenv" {
		t.Errorf("Server = %q, want the .env to win", o.Config.Server)
	}
}

// An unknown key is a hard failure and not a warning, because the typo that
// actually happens changes behaviour silently: SQL_LOGIN instead of SQL_USER
// falls through to integrated authentication, and the run then measures the
// wrong login's permissions.
func TestBuildOptionsRefusesAnUnknownDotEnvKey(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\nSQL_LOGIN=sa\n")
	_, code, err := buildOptions("check", []string{"--env", env}, noEnv, noStdin)
	if err == nil {
		t.Fatal("an unknown .env key was accepted")
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), "SQL_LOGIN") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

// The wizard calls buildOptions with no command line at all, before the operator
// has typed anything. That path must produce a usable Options rather than a
// refusal, or the first screen would have nothing to display.
func TestBuildOptionsResolvesWithoutAnyArguments(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "absent.env")
	exported := func(k string) string {
		switch k {
		case "SQL_SERVER":
			return "invalid.invalid"
		case "SQL_CONNECT_TIMEOUT_SEC":
			return "1"
		}
		return ""
	}
	o, code, err := buildOptions("collect", []string{"--env", env}, exported, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.Server != "invalid.invalid" {
		t.Errorf("Server = %q", o.Config.Server)
	}
	if o.Corpus == nil || o.Root != "queries" {
		t.Errorf("the embedded corpus was not selected: root %q", o.Root)
	}
	if o.Version == "" {
		t.Error("Version is empty; every archive records the build that made it")
	}
}

// writePasswordFile puts a file next to nothing else and returns its path. The
// body is written verbatim, because what these tests are about is exactly which
// bytes survive the read.
func writePasswordFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A password read from a file is a flag, so it beats the .env for the same
// reason --server does: it is what the operator typed for this run.
func TestPasswordFileBeatsTheDotEnvFile(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\nSQL_USER=sa\nSQL_PASSWORD=from-dotenv\n")
	pw := writePasswordFile(t, "from-file")
	o, code, err := buildOptions("collect", []string{"--env", env, "--password-file", pw}, noEnv, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.Password != "from-file" {
		t.Errorf("Password = %q, want the file to win", o.Config.Password)
	}
}

// Exactly one trailing line ending goes, and nothing else. A password may
// legitimately end in a space, contain a "#", or hold a second line's worth of
// punctuation; trimming more than the line ending an editor added would corrupt
// passwords that work everywhere else.
func TestPasswordFileTrimsOneLineEndingAndNothingElse(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"no line ending", "p@ss w0rd", "p@ss w0rd"},
		{"unix line ending", "p@ss w0rd\n", "p@ss w0rd"},
		{"windows line ending", "p@ss w0rd\r\n", "p@ss w0rd"},
		{"a trailing space is part of the password", "p@ss \n", "p@ss "},
		{"a hash is not a comment", "p@ss#1\n", "p@ss#1"},
		{"only the last line ending goes", "p@ss\n\n", "p@ss\n"},
	}
	for _, c := range cases {
		env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\nSQL_USER=sa\n")
		o, code, err := buildOptions("collect",
			[]string{"--env", env, "--password-file", writePasswordFile(t, c.body)}, noEnv, noStdin)
		if err != nil || code != 0 {
			t.Fatalf("%s: buildOptions: code %d, err %v", c.name, code, err)
		}
		if o.Config.Password != c.want {
			t.Errorf("%s: Password = %q, want %q", c.name, o.Config.Password, c.want)
		}
	}
}

// --password-stdin reads the same way, from the reader the caller supplies.
func TestPasswordStdinReadsTheSuppliedReader(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\nSQL_USER=sa\nSQL_PASSWORD=from-dotenv\n")
	o, code, err := buildOptions("collect", []string{"--env", env, "--password-stdin"},
		noEnv, strings.NewReader("from-stdin\n"))
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.Password != "from-stdin" {
		t.Errorf("Password = %q, want the reader to win", o.Config.Password)
	}
}

// An empty source is refused rather than silently meaning "no password". The
// mistake that produces it — a file the CI wrote nothing into, a pipeline whose
// left-hand side failed — would otherwise fall through to integrated
// authentication and measure the wrong login, which is the same failure the
// unknown-key refusal exists to prevent.
func TestPasswordSourcesRefuseWhatTheyCannotRead(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\nSQL_USER=sa\n")
	cases := []struct {
		name  string
		args  []string
		stdin *strings.Reader
		names string
	}{
		{"an empty file", []string{"--password-file", writePasswordFile(t, "")}, noStdin, "empty"},
		{"a file holding only a line ending", []string{"--password-file", writePasswordFile(t, "\n")}, noStdin, "empty"},
		{"an absent file", []string{"--password-file", filepath.Join(t.TempDir(), "absent")}, noStdin, "absent"},
		{"an empty stdin", []string{"--password-stdin"}, strings.NewReader(""), "empty"},
	}
	for _, c := range cases {
		_, code, err := buildOptions("collect", append([]string{"--env", env}, c.args...), noEnv, c.stdin)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if code != 2 {
			t.Errorf("%s: exit code = %d, want 2", c.name, code)
		}
	}
}

// Both at once is a refusal and not a precedence rule. Two sources named on one
// command line is a mistake in whatever generated it, and picking a winner would
// hide it.
func TestPasswordFileAndStdinTogetherAreRefused(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\nSQL_USER=sa\n")
	pw := writePasswordFile(t, "from-file")
	_, code, err := buildOptions("collect",
		[]string{"--env", env, "--password-file", pw, "--password-stdin"},
		noEnv, strings.NewReader("from-stdin\n"))
	if err == nil {
		t.Fatal("both password sources were accepted at once")
	}
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), "--password-file") || !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("error %q does not name both options", err)
	}
}

// The password is never printed back. It reaches Config and stops there: no
// echo, no confirmation line, nothing an operator could paste into a bug report
// by accident.
func TestPasswordNeverReachesTheFlagsUsageText(t *testing.T) {
	c := defineFlags("collect")
	var b strings.Builder
	c.fs.SetOutput(&b)
	c.fs.PrintDefaults()
	if strings.Contains(b.String(), "--password ") {
		t.Error("a bare --password option exists; README says it must not")
	}
}
