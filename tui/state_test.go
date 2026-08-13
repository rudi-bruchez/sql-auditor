package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

// errConnRefusedForTest stands in for what the driver hands back. Nothing here
// opens a connection: the tests of this package never touch a network, and no
// test in this file has an address to resolve.
var errConnRefusedForTest = errors.New("login failed for user 'AUDIT_RO'")

// named and typed keep the transition tables readable: every case in this file
// is "one keystroke applied to one state", and spelling out screen.Key on each
// line would bury the interesting half.
func named(n screen.NamedKey) screen.Key { return screen.Key{Named: n} }
func typed(r rune) screen.Key            { return screen.Key{Rune: r} }

func TestEnterOnTheConnectionScreenWaitsForTheConnection(t *testing.T) {
	s := State{Step: StepConnection}
	got := s.Key(named(screen.KeyEnter))
	// Not StepVerification: opening a connection to a frozen instance takes
	// as long as the connect timeout allows, and a screen that jumped
	// straight to the verification would be a frozen screen.
	if got.Step != StepConnecting {
		t.Fatalf("Step = %v, want StepConnecting", got.Step)
	}
}

func TestQuitFromTheEarlyStepsLeavesTheWizard(t *testing.T) {
	for _, step := range []Step{StepVerification, StepOptions} {
		s := State{Step: step}
		if got := s.Key(typed('q')); got.Step != StepQuit {
			t.Errorf("from %v: Step = %v, want StepQuit", step, got.Step)
		}
	}
}

func TestSpaceTogglesTheSelectedFlag(t *testing.T) {
	s := State{Step: StepOptions, Flags: map[string]bool{}, FlagIndex: 1}
	got := s.Key(named(screen.KeySpace))
	if !got.Flags[flagOrder[1]] {
		t.Fatalf("Flags[%s] = false, want true", flagOrder[1])
	}
	// The map must not be shared with the state it came from: the wizard keeps
	// previous states around only by accident, but a shared map would make
	// every past state change under the caller's feet.
	if s.Flags[flagOrder[1]] {
		t.Error("the source state's map was mutated in place")
	}
}

func TestTurningOffQueryStoreDetailTurnsOffPlanStats(t *testing.T) {
	flags := map[string]bool{
		collect.FlagQueryStoreDetail:    true,
		collect.FlagQueryStorePlanStats: true,
	}
	got := toggleFlag(flags, collect.FlagQueryStoreDetail)
	if got[collect.FlagQueryStoreDetail] {
		t.Error("query_store_detail stayed on")
	}
	if got[collect.FlagQueryStorePlanStats] {
		t.Error("query_store_plan_stats survived its prerequisite")
	}
}

func TestPlanStatsRefusesToTurnOnAlone(t *testing.T) {
	got := toggleFlag(map[string]bool{}, collect.FlagQueryStorePlanStats)
	if got[collect.FlagQueryStorePlanStats] {
		t.Error("query_store_plan_stats turned on without query_store_detail")
	}
	on := toggleFlag(map[string]bool{collect.FlagQueryStoreDetail: true}, collect.FlagQueryStorePlanStats)
	if !on[collect.FlagQueryStorePlanStats] {
		t.Error("query_store_plan_stats refused to turn on beside its prerequisite")
	}
}

func TestCtrlCDuringCollectionAsksToStopWithoutLeavingTheScreen(t *testing.T) {
	s := State{Step: StepCollecting}
	got := s.Key(named(screen.KeyCtrlC))
	// The run keeps going until collect.Run returns: it still has a manifest
	// and an archive to write, and the screen has to say so meanwhile.
	if got.Step != StepCollecting {
		t.Fatalf("Step = %v, want StepCollecting", got.Step)
	}
	if !got.Stopping {
		t.Error("Stopping = false, want true")
	}
}

func TestCtrlCDuringVerificationReturnsToTheConnectionScreen(t *testing.T) {
	for _, step := range []Step{StepConnecting, StepVerifying} {
		s := State{Step: step}
		got := s.Key(named(screen.KeyCtrlC))
		if got.Step != StepConnection {
			t.Errorf("from %v: Step = %v, want StepConnection", step, got.Step)
		}
	}
}

func TestTheCollisionPromptRequiresAKeystrokeThatNamesTheChoice(t *testing.T) {
	base := State{
		Step:      StepOptions,
		Collision: `C:\out\SQL01_PROD-2026-08-13.zip`,
		Verify:    collect.VerifyResult{Probed: true, Collectors: 47},
	}
	cases := []struct {
		key       screen.Key
		wantStep  Step
		wantKeep  bool
		wantStart bool
	}{
		{named(screen.KeyEnter), StepCollecting, false, true},
		{typed('k'), StepCollecting, true, true},
		{typed('b'), StepVerification, false, false},
		{typed('x'), StepOptions, false, false},
		{named(screen.KeySpace), StepOptions, false, false},
	}
	for _, c := range cases {
		got := base.Key(c.key)
		if got.Step != c.wantStep {
			t.Errorf("key %+v: Step = %v, want %v", c.key, got.Step, c.wantStep)
		}
		if got.Keep != c.wantKeep {
			t.Errorf("key %+v: Keep = %v, want %v", c.key, got.Keep, c.wantKeep)
		}
		if c.wantStart && got.Collision != "" {
			t.Errorf("key %+v: the prompt survived the choice it answered", c.key)
		}
	}
}

func TestTabCyclesOverTheTwoEditableFields(t *testing.T) {
	s := State{Step: StepConnection}
	seen := []int{}
	for i := 0; i < 4; i++ {
		s = s.Key(named(screen.KeyTab))
		seen = append(seen, s.Field)
	}
	// Two fields, not five: the database, the authentication mode and the
	// encryption settings are shown and not edited.
	want := []int{fieldPassword, fieldServer, fieldPassword, fieldServer}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("tab %d landed on field %d, want %d", i+1, seen[i], want[i])
		}
	}
}

func TestTypingGoesToTheSelectedField(t *testing.T) {
	s := State{Step: StepConnection}
	for _, r := range "SRV" {
		s = s.Key(typed(r))
	}
	s = s.Key(named(screen.KeyTab))
	// A space is a keystroke like any other while editing: passwords contain
	// them, and a wizard that swallowed the space would refuse a password the
	// operator cannot type any other way.
	for _, k := range []screen.Key{typed('p'), named(screen.KeySpace), typed('w')} {
		s = s.Key(k)
	}
	if s.Server != "SRV" {
		t.Errorf("Server = %q, want %q", s.Server, "SRV")
	}
	if s.Password != "p w" {
		t.Errorf("Password = %q, want %q", s.Password, "p w")
	}
	if s.Step != StepConnection {
		t.Errorf("typing left the screen: Step = %v", s.Step)
	}
}

func TestBackspaceRemovesAWholeRune(t *testing.T) {
	// A password of "clé" is three runes and four bytes. Dropping one byte
	// would leave invalid UTF-8 in the connection string and a refusal the
	// operator cannot explain, because the screen shows nothing but stars.
	s := State{Step: StepConnection, Field: fieldPassword, Password: "clé"}
	s = s.Key(named(screen.KeyBackspace))
	if s.Password != "cl" {
		t.Fatalf("Password = %q, want %q", s.Password, "cl")
	}
	empty := State{Step: StepConnection}.Key(named(screen.KeyBackspace))
	if empty.Server != "" {
		t.Errorf("backspace on an empty field produced %q", empty.Server)
	}
}

func TestTypingOverAFlagValueMarksItOverridden(t *testing.T) {
	s := State{Step: StepConnection, Server: "SRV-FROM-FLAG", ServerFromFlag: true}
	if s.Key(named(screen.KeyTab)).ServerOverridden {
		t.Error("merely moving between fields marked the server overridden")
	}
	got := s.Key(typed('X'))
	if !got.ServerOverridden {
		t.Error("ServerOverridden = false after typing over a --server value")
	}
	// Erasing the flag's value is an edit too, and the one most likely to end
	// up pointing somewhere else entirely.
	if !s.Key(named(screen.KeyBackspace)).ServerOverridden {
		t.Error("ServerOverridden = false after erasing a --server value")
	}
}

func TestARefusedConnectionStaysOnTheConnectionScreen(t *testing.T) {
	s := State{Step: StepConnecting, Server: "invalid.invalid", Field: fieldPassword}
	got := s.connectFailed(errConnRefusedForTest)
	if got.Step != StepConnection {
		t.Fatalf("Step = %v, want StepConnection", got.Step)
	}
	if got.ConnError == nil {
		t.Error("ConnError is nil, want the server's refusal")
	}
	// The cursor goes back to the server field: the address is what the
	// operator is most likely to be fixing, and it is the field a wrong
	// password does not explain.
	if got.Field != fieldServer {
		t.Errorf("Field = %d, want fieldServer", got.Field)
	}
	if got.Server != s.Server {
		t.Errorf("the refusal cleared the address: %q", got.Server)
	}
}

func TestPasswordNeverAppearsInAnyRenderedLine(t *testing.T) {
	// The rendered half of this claim is asserted in render_test.go. What is
	// assertable here is the shape that makes it possible: the password lives
	// in exactly one field, and nothing — not a failed connection, not a step
	// change — copies it anywhere else.
	const secret = "hunter2"
	s := State{Step: StepConnection, Field: fieldPassword}
	for _, r := range secret {
		s = s.Key(typed(r))
	}
	s = s.Key(named(screen.KeyEnter)).connectFailed(errConnRefusedForTest)
	if s.Password != secret {
		t.Fatalf("Password = %q, want %q", s.Password, secret)
	}
	if s.Server == secret || s.ConnError.Error() == secret {
		t.Error("the password leaked into a field the screens print")
	}
}

func TestTheSameDayArchiveIsDetectedBeforeAnythingIsDestroyed(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)
	if got := collisionFor(dir, `SQL01\PROD`, now, false); got != "" {
		t.Fatalf("collisionFor on an empty directory = %q, want %q", got, "")
	}
	// The archive alone is enough. It sits beside the run folder rather than
	// inside it, and it is the file that was mailed onward.
	zip := filepath.Join(dir, collect.RunFolderName(`SQL01\PROD`, now)+".zip")
	if err := os.WriteFile(zip, []byte("previous run"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := collisionFor(dir, `SQL01\PROD`, now, false)
	if !strings.Contains(got, zip) {
		t.Fatalf("collisionFor = %q, want it to name %q", got, zip)
	}
	// --keep suffixes its way to a free name, so there is nothing in the way
	// and nothing to ask about.
	if kept := collisionFor(dir, `SQL01\PROD`, now, true); kept != "" {
		t.Errorf("collisionFor under keep = %q, want %q", kept, "")
	}

	// Going back destroys nothing: the state machine never touches the disk,
	// and the archive is still there for the operator who chose [b].
	s := State{Step: StepOptions, Collision: got}
	if back := s.Key(typed('b')); back.Step != StepVerification {
		t.Fatalf("[b] led to %v, want StepVerification", back.Step)
	}
	if _, err := os.Stat(zip); err != nil {
		t.Errorf("the archive did not survive the prompt: %v", err)
	}
}

func TestFlagOrderListsTheSevenOptInsInScreenOrder(t *testing.T) {
	want := []string{
		collect.FlagIncludeSessionText,
		collect.FlagObjectDefinitions,
		collect.FlagDeadlockGraphs,
		collect.FlagBlockedProcessReports,
		collect.FlagQueryStoreDetail,
		collect.FlagQueryStorePlanStats,
		collect.FlagEstimateCompression,
	}
	if len(flagOrder) != len(want) {
		t.Fatalf("flagOrder has %d entries, want %d", len(flagOrder), len(want))
	}
	for i := range want {
		if flagOrder[i] != want[i] {
			t.Errorf("flagOrder[%d] = %q, want %q", i, flagOrder[i], want[i])
		}
	}
}
