package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTheHelpNamesTheBuildItCameFrom(t *testing.T) {
	// Which build produced this output is the first question asked of a run
	// that disagrees with another, and until now only check and collect
	// answered it. Every path that prints the help now does too.
	var buf bytes.Buffer
	writeUsage(&buf)

	first, _, _ := strings.Cut(buf.String(), "\n")
	if !strings.Contains(first, version) {
		t.Errorf("the help does not open with the version: %q", first)
	}
	if !strings.Contains(buf.String(), "sql-auditor collect") {
		t.Error("the help lost its body")
	}
}

func TestTheVersionIsSpeltTheSameEverywhere(t *testing.T) {
	// The banner is one function because it appears in four places — the help,
	// the argument-less refusal, check and collect — and four literals would
	// eventually disagree about the format of the commit.
	b := banner()
	if !strings.Contains(b, version) || !strings.Contains(b, buildStamp()) {
		t.Errorf("banner() = %q, want the version and the build stamp", b)
	}
}

func TestTheUsualWaysOfAskingForTheVersionAreAccepted(t *testing.T) {
	// `sql-auditor --version` used to answer `"--version" is not a command …
	// Did you mean "version"?`. The reflex is universal and the suggestion is
	// one round trip for nothing.
	cases := []struct {
		in   string
		want bool
	}{
		{"--version", true},
		{"-version", true},
		{"-V", true},
		{"--V", true},
		{"version", false}, // the command itself; the dispatch handles it
		// Lowercase -v is deliberately NOT the version. It is the reflex for
		// "verbose" everywhere else, and this program has just acquired a
		// verbose mode for it to be confused with.
		{"-v", false},
		{"--verbose", false},
		{"check", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isVersionSwitch(c.in); got != c.want {
			t.Errorf("isVersionSwitch(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAnArgumentLessRunSaysWhatIsMissing(t *testing.T) {
	// The complaint this answers: the tool answered a bare `sql-auditor` with
	// ninety lines of options and not one word about what it had looked for.
	// The reader was left to work out that .env is a thing and that it was not
	// there.
	noFile := func(string) bool { return false }
	aFile := func(string) bool { return true }
	withServer := func(k string) string {
		if k == "SQL_SERVER" {
			return "sql01"
		}
		return ""
	}

	cases := []struct {
		name        string
		exists      func(string) bool
		env         func(string) string
		wantPhrases []string
	}{
		{
			"neither a file nor a variable",
			noFile, noEnv,
			// The next command has to be in the line. "nothing is configured"
			// on its own is a diagnosis with no cure.
			[]string{"no .env", "SQL_SERVER", "sql-auditor env init"},
		},
		{
			"a .env in the current directory",
			aFile, noEnv,
			[]string{".env", "sql-auditor check"},
		},
		{
			"the variable exported",
			noFile, withServer,
			[]string{"SQL_SERVER", "sql-auditor check"},
		},
		{
			"both",
			aFile, withServer,
			[]string{"sql-auditor check"},
		},
	}
	for _, c := range cases {
		got := nothingToDo(c.exists, c.env)
		for _, p := range c.wantPhrases {
			if !strings.Contains(got, p) {
				t.Errorf("%s: %q does not mention %q", c.name, got, p)
			}
		}
	}
}

func TestTheDiagnosisNeverPointsAtACommandThatWouldFail(t *testing.T) {
	// `sql-auditor check` with nothing configured refuses with "SQL_SERVER is
	// not set", so sending an operator there from a screen that has just told
	// them nothing is configured would be a second dead end.
	got := nothingToDo(func(string) bool { return false }, noEnv)
	if strings.Contains(got, "sql-auditor check") {
		t.Errorf("the empty case points at check: %q", got)
	}
}
