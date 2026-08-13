package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// The width every witness string in this file is measured at. Eighty columns
// is what a default terminal gives and what a screenshot in a bug report will
// have been taken at.
const testWidth = 80

// joined is the whole frame as one string, for the assertions that only care
// whether something is present anywhere.
func joined(lines []string) string { return strings.Join(lines, "\n") }

func contains(t *testing.T, lines []string, want string) {
	t.Helper()
	if !strings.Contains(joined(lines), want) {
		t.Fatalf("no line carries %q; frame was:\n%s", want, joined(lines))
	}
}

func absent(t *testing.T, lines []string, unwanted string) {
	t.Helper()
	if strings.Contains(joined(lines), unwanted) {
		t.Fatalf("frame carries %q and must not; frame was:\n%s", unwanted, joined(lines))
	}
}

// probedVerify is a verification that succeeded: eight checks in probe order,
// two of them denied, twelve databases and three without access.
func probedVerify() collect.VerifyResult {
	v := collect.VerifyResult{
		Probed:     true,
		Server:     collect.ServerInfo{Name: `SQL01\PROD`, Version: "15.0.4345", Edition: "Standard Edition", Login: "AUDIT_RO"},
		Collectors: 47,
		NoAccess:   []string{"FACTURATION", "RH", "ARCHIVE_2019"},
	}
	for _, c := range collect.Capabilities() {
		check := collect.CapabilityCheck{Name: c.Name, Label: c.Label, Status: "ok"}
		if c.Name == "agent_job_steps" || c.Name == "log_shipping" {
			check.Status, check.Impact = "denied", c.Impact
		}
		v.Checks = append(v.Checks, check)
	}
	for i := 0; i < 12; i++ {
		v.Selection.Included = append(v.Selection.Included, "DB")
		v.Folders = append(v.Folders, collect.DatabaseFolder{Name: "DB", Folder: "DB"})
	}
	for _, n := range v.NoAccess {
		v.Selection.Skipped = append(v.Selection.Skipped, collect.SkipReason{Name: n, Reason: collect.SkipNoAccess})
	}
	return v
}

// statusLines extracts, in order, the capability rows of a frame: the lines
// whose first word is one of the four status words the screens use.
//
// It exists instead of strings.Index on the capability names, which would be a
// trap: "connect" is a substring of nothing else here, but "view_server_state"
// and "connect to the instance" both appear in labels, and an index-based order
// check passes on a frame that prints the eight capabilities in the wrong order
// as long as some other text happens to fall in the right places.
func statusLines(lines []string) [][2]string {
	var out [][2]string
	for _, l := range lines {
		t := strings.TrimLeft(l, " ")
		for _, st := range []string{"not checked", "denied", "error", "ok"} {
			if !strings.HasPrefix(t, st+" ") {
				continue
			}
			name := strings.TrimSpace(strings.TrimPrefix(t, st))
			out = append(out, [2]string{st, name})
			break
		}
	}
	return out
}

// A resize of an RDP window makes GetSize report 0x0 while the loop keeps
// repainting. Every step has to survive it: a panic there leaves a terminal in
// raw mode with no wizard left to restore it.
func TestRenderSurvivesAZeroSizedTerminal(t *testing.T) {
	steps := []Step{StepConnection, StepConnecting, StepVerification, StepVerifying,
		StepOptions, StepCollecting, StepDone, StepQuit}
	for _, st := range steps {
		s := State{Step: st, Verify: probedVerify(), Units: 223, DoneUnits: 147,
			Flags: map[string]bool{}, ZipPath: `C:\out\a.zip`}
		Render(s, 0, 0)
		// And with a probe that failed, which is the other half of every screen.
		Render(State{Step: st, Flags: map[string]bool{}}, 0, 0)
	}
}

func TestVerificationListsTheEightCapabilitiesInProbeOrder(t *testing.T) {
	s := State{Step: StepVerification, Verify: probedVerify()}
	got := statusLines(Render(s, testWidth, 0))

	caps := collect.Capabilities()
	if len(got) != len(caps) {
		t.Fatalf("got %d capability lines, want %d: %v", len(got), len(caps), got)
	}
	for i, c := range caps {
		if got[i][1] != c.Name {
			t.Fatalf("capability %d is %q, want %q", i, got[i][1], c.Name)
		}
	}
	// The total is derived from Capabilities(), never written down.
	contains(t, Render(s, testWidth, 0), "6 / 8")
}

func TestVerificationKeepsTheWholeImpactAtSixtyColumns(t *testing.T) {
	lines := Render(State{Step: StepVerification, Verify: probedVerify()}, 60, 0)
	// Every word of the impact survives, in order, across the fold. Truncating
	// it would reverse its meaning, which is the whole reason impacts are
	// wrapped and columns are not.
	var impact string
	for _, c := range collect.Capabilities() {
		if c.Name == "log_shipping" {
			impact = c.Impact
		}
	}
	flat := strings.Join(strings.Fields(joined(lines)), " ")
	if !strings.Contains(flat, strings.Join(strings.Fields(impact), " ")) {
		t.Fatalf("the log_shipping impact did not survive the fold; frame was:\n%s", joined(lines))
	}
	absent(t, lines, "…")
}

func TestOptionsTakesTheCollectorCountFromTheResolvedPlan(t *testing.T) {
	v := probedVerify()
	v.Collectors = 47
	lines := Render(State{Step: StepOptions, Verify: v, Flags: map[string]bool{},
		QueryStoreWindow: "last 7 days (from .env)"}, testWidth, 0)
	contains(t, lines, "47 collectors")
	// 55 is the number of files in the corpus, and announcing it here would be
	// contradicted by the gauge on the next screen.
	absent(t, lines, "55")
	contains(t, lines, "Query Store window: last 7 days (from .env)")
}

func TestConnectionMarksAServerTypedOverAFlag(t *testing.T) {
	s := State{Step: StepConnection, Server: `SQL01\PROD`, ServerFromFlag: true, ServerOverridden: true,
		Catalog: "master", User: "AUDIT_RO", Encrypt: true, TrustCert: true}
	lines := Render(s, testWidth, 0)
	contains(t, lines, "(overrides --server)")
	contains(t, lines, "yes, server certificate NOT validated")
	contains(t, lines, "[esc] quit")
	// [q] would close the wizard for anybody connecting to a server named
	// QUALIF, so screen 1 does not offer it.
	absent(t, lines, "[q] quit")
}

func TestVerificationSaysNotCheckedAndOffersNoContinuation(t *testing.T) {
	s := State{Step: StepVerification, Verify: collect.VerifyResult{ServerErr: errors.New("login failed for user 'AUDIT_RO'")}}
	lines := Render(s, testWidth, 0)

	for i, sl := range statusLines(lines) {
		if sl[0] != "not checked" {
			t.Fatalf("capability %d reports %q on an unprobed instance, want \"not checked\"", i, sl[0])
		}
	}
	contains(t, lines, "login failed for user")
	// Nothing may be collected from an instance nobody could describe.
	absent(t, lines, "[enter] continue")
	contains(t, lines, "[r] re-probe")
	contains(t, lines, "[b] back")
	// A zero would read as a measurement: no permissions denied, no databases.
	absent(t, lines, "0 selected")
}

func TestOptionsRefusesToStartWithNothingToCollect(t *testing.T) {
	v := collect.VerifyResult{Probed: true, Collectors: 0}
	lines := Render(State{Step: StepOptions, Verify: v, Flags: map[string]bool{}}, testWidth, 0)
	contains(t, lines, "Nothing to collect: no database selected")
	absent(t, lines, "[enter] start collection")

	v.Folders = []collect.DatabaseFolder{{Name: "SALESDB", Folder: "SALESDB"}}
	lines = Render(State{Step: StepOptions, Verify: v, Flags: map[string]bool{}}, testWidth, 0)
	contains(t, lines, "Nothing to collect: no collector runs on this version")
	absent(t, lines, "[enter] start collection")
}

func TestOptionsShowsTheSameDayCollisionInsteadOfStarting(t *testing.T) {
	lines := Render(State{Step: StepOptions, Verify: probedVerify(), Flags: map[string]bool{},
		Collision: `C:\out\SQL01_PROD-2026-08-13.zip`}, testWidth, 0)
	contains(t, lines, "A run of the same day already exists:")
	contains(t, lines, `C:\out\SQL01_PROD-2026-08-13.zip`)
	contains(t, lines, "[enter] replace it   [k] keep both   [b] back")
	absent(t, lines, "[enter] start collection")
}

func TestOptionsTicksTheFlagsThatAreOn(t *testing.T) {
	s := State{Step: StepOptions, Verify: probedVerify(), FlagIndex: 1,
		Flags: map[string]bool{collect.FlagQueryStoreDetail: true}}
	lines := Render(s, testWidth, 0)
	contains(t, lines, "[x] query store detail")
	contains(t, lines, "> [ ] object definitions")
	contains(t, lines, "[ ] plan stats")
}

// The password is the reason screen 1 is editable at all, and the one value in
// this wizard that must never be written anywhere: not to disk, not to the
// manifest, and not to a line that could end up in a screenshot or in the
// scrollback of a shared RDP session.
func TestPasswordNeverAppearsInAnyRenderedLineOfAnyScreen(t *testing.T) {
	const secret = "Tr0ub4dor&3"
	steps := []Step{StepConnection, StepConnecting, StepVerification, StepVerifying,
		StepOptions, StepCollecting, StepDone, StepQuit}
	for _, st := range steps {
		for _, w := range []int{0, 40, 200} {
			s := State{Step: st, Password: secret, Server: "invalid.invalid", Verify: probedVerify(),
				Flags: map[string]bool{}, Field: fieldPassword}
			if strings.Contains(joined(Render(s, w, 0)), secret) {
				t.Fatalf("step %d at width %d rendered the password", st, w)
			}
		}
	}
	// It is masked, not dropped: a field that shows nothing at all is
	// indistinguishable from a dead keyboard on a value nobody can read back.
	contains(t, Render(State{Step: StepConnection, Password: secret}, testWidth, 0), strings.Repeat("*", len(secret)))
}

func TestASCIIRenderingHasNoHighBytes(t *testing.T) {
	s := State{Step: StepVerification, ASCII: true, Verify: probedVerify()}
	for _, l := range Render(s, testWidth, 0) {
		for i := 0; i < len(l); i++ {
			if l[i] >= 0x80 {
				t.Fatalf("byte %#x in an ASCII frame: %q", l[i], l)
			}
		}
	}
	// The em dash of the impacts is what the fold is for; nothing else in the
	// screens is non-ASCII.
	contains(t, Render(s, testWidth, 0), "not collected - the report")
}

func TestBlockedProcessCaptureComesFromCollect(t *testing.T) {
	v := probedVerify()
	v.Blocking = collect.BlockingReadiness{Probed: true} // nothing subscribed, threshold 0
	lines := Render(State{Step: StepVerification, Verify: v}, testWidth, 0)
	contains(t, lines, "Blocked process capture")
	contains(t, lines, "not ready")
	contains(t, lines, "no Extended Events session subscribes to blocked_process_report")
	contains(t, lines, collect.BlockingHowTo)
	// No lock waits recorded: the figures are an absent measurement, not a
	// measurement of zero.
	absent(t, lines, "Lock waits:")

	v.Blocking.LockWaitMS, v.Blocking.TotalWaitMS, v.Blocking.LockWaitTasks = 7_200_000, 72_000_000, 400
	contains(t, Render(State{Step: StepVerification, Verify: v}, testWidth, 0), "Lock waits:")
}

func TestNoFrameCarriesAnEscapeSequence(t *testing.T) {
	steps := []Step{StepConnection, StepConnecting, StepVerification, StepVerifying, StepOptions}
	for _, st := range steps {
		s := State{Step: st, Verify: probedVerify(), Flags: map[string]bool{}}
		if strings.ContainsRune(joined(Render(s, testWidth, 0)), 0x1b) {
			t.Fatalf("step %d rendered an escape sequence; there is no colour in this package", st)
		}
	}
}

func TestRenderCutsAFrameToTheTerminalHeight(t *testing.T) {
	lines := Render(State{Step: StepVerification, Verify: probedVerify()}, testWidth, 5)
	if len(lines) != 5 {
		t.Fatalf("got %d lines for a five-line terminal", len(lines))
	}
}
