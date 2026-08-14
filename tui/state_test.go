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

// The collision must not become a mode. It used to return early, which cost
// three things at once — and all three land on the operator this wizard exists
// for, on the one run they will make.
func TestTheCollisionDoesNotTakeOverTheKeyboard(t *testing.T) {
	base := State{
		Step:      StepOptions,
		Collision: `C:\out\SQL01_PROD-2026-08-13.zip`,
		Verify:    collect.VerifyResult{Probed: true, Collectors: 47},
		Flags:     map[string]bool{},
	}

	// [tab] and [space] keep working. Rerunning the same day TO TICK AN OPTION
	// is the scenario the collision exists for; a prompt that swallowed them
	// would make it unreachable.
	moved := base.Key(named(screen.KeyTab))
	if moved.FlagIndex != 1 || moved.Step != StepOptions {
		t.Errorf("[tab] under a collision: FlagIndex = %d, Step = %v", moved.FlagIndex, moved.Step)
	}
	if ticked := base.Key(named(screen.KeySpace)); !ticked.Flags[flagOrder[0]] {
		t.Error("[space] under a collision toggled nothing")
	}

	// Ctrl-C still quits. watchSignals folds SIGINT and SIGTERM onto this key,
	// so a screen that ate it would leave the process killable only by -9,
	// which leaves the terminal in raw mode.
	if got := base.Key(named(screen.KeyCtrlC)); got.Step != StepQuit {
		t.Errorf("ctrl-c under a collision led to %v, want StepQuit", got.Step)
	}

	// And [enter] still obeys the gate. On a 2012 instance where every version
	// gate closes, replacing an archive to produce one holding nothing but a
	// manifest is the round trip canStart() exists to remove.
	empty := base
	empty.Verify.Collectors = 0
	for _, k := range []screen.Key{named(screen.KeyEnter), typed('k')} {
		if got := empty.Key(k); got.Step != StepOptions {
			t.Errorf("key %+v with nothing to collect led to %v, want StepOptions", k, got.Step)
		}
	}

	// [k] means nothing when nothing asked: outside a collision it must not
	// quietly turn --keep on.
	free := base
	free.Collision = ""
	if got := free.Key(typed('k')); got.Keep || got.Step != StepOptions {
		t.Errorf("[k] with no collision: Keep = %v, Step = %v", got.Keep, got.Step)
	}
}

// Ctrl-C ends the wizard on the final screen too. It carries no rune, so a
// case testing only [enter] and 'q' ignored it — and watchSignals folds SIGINT
// and SIGTERM onto the same key, so an external kill went unanswered on the
// screen the operator sits on longest.
func TestTheFinalScreenAnswersCtrlCLikeEveryOther(t *testing.T) {
	for _, k := range []screen.Key{named(screen.KeyEnter), named(screen.KeyCtrlC), typed('q')} {
		if got := (State{Step: StepDone}).Key(k); got.Step != StepQuit {
			t.Errorf("key %+v on the final screen led to %v, want StepQuit", k, got.Step)
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

// probedAt builds the state and options of an instance the operator reached by
// address and whose real name the probe brought back. The two spellings differ
// on purpose: that difference is what every test below is about.
func probedAt(dir string, now time.Time, keep bool) (State, collect.Options) {
	s := State{
		Step: StepOptions,
		// What the DBA typed. RunFolderFor must never see it.
		Server: "10.42.7.19,1433",
		Keep:   keep,
		Verify: collect.VerifyResult{
			Probed:     true,
			Collectors: 47,
			Server:     collect.ServerInfo{Name: `SQL01\PROD`},
		},
	}
	return s, collect.Options{
		Config: &collect.Config{OutputDir: dir, Server: s.Server},
		Now:    now,
		Keep:   keep,
	}
}

// The wizard must ask its question about the path collect.Run will actually
// use. Run names its folder from SERVERPROPERTY('ServerName'); an address or an
// FQDN produces a different name, so a wizard that asked about the typed
// address would find every path free, print no prompt, and let
// prepareRunFolder delete the previous run's folder and archive in silence.
func TestTheRunFolderIsNamedAfterTheProbedServerAndNotTheTypedAddress(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)
	s, o := probedAt(dir, now, false)

	want := collect.RunFolderFor(dir, s.Verify.Server.Name, now, false)
	if got := runFolderFor(s, o); got != want {
		t.Fatalf("runFolderFor = %q, want the path collect.Run will use, %q", got, want)
	}
	if typed := collect.RunFolderFor(dir, s.Server, now, false); typed == want {
		t.Fatal("the two spellings coincide; this test proves nothing as written")
	}
}

func TestTheSameDayArchiveIsDetectedBeforeAnythingIsDestroyed(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)
	s, o := probedAt(dir, now, false)
	if got := collisionFor(s, o); got != "" {
		t.Fatalf("collisionFor on an empty directory = %q, want %q", got, "")
	}
	// The archive alone is enough. It sits beside the run folder rather than
	// inside it, and it is the file that was mailed onward.
	zip := filepath.Join(dir, collect.RunFolderName(`SQL01\PROD`, now)+".zip")
	if err := os.WriteFile(zip, []byte("previous run"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := collisionFor(s, o)
	if !strings.Contains(got, zip) {
		t.Fatalf("collisionFor = %q, want it to name %q", got, zip)
	}
	// --keep suffixes its way to a free name, so there is nothing in the way
	// and nothing to ask about.
	ks, ko := probedAt(dir, now, true)
	if kept := collisionFor(ks, ko); kept != "" {
		t.Errorf("collisionFor under keep = %q, want %q", kept, "")
	}
	// An unprobed instance has no name to build a path from, and screen 3 can
	// start nothing anyway.
	unprobed, uo := probedAt(dir, now, false)
	unprobed.Verify = collect.VerifyResult{}
	if none := collisionFor(unprobed, uo); none != "" {
		t.Errorf("collisionFor without a probe = %q, want %q", none, "")
	}

	// Going back destroys nothing: the state machine never touches the disk,
	// and the archive is still there for the operator who chose [b].
	s.Collision = got
	if back := s.Key(typed('b')); back.Step != StepVerification {
		t.Fatalf("[b] led to %v, want StepVerification", back.Step)
	}
	if _, err := os.Stat(zip); err != nil {
		t.Errorf("the archive did not survive the prompt: %v", err)
	}
}

func TestBackGoesUpOneStepAndNoFurtherThanTheFirst(t *testing.T) {
	if got := (State{Step: StepVerification}).Key(typed('b')); got.Step != StepConnection {
		t.Errorf("from step 2: Step = %v, want StepConnection", got.Step)
	}
	if got := (State{Step: StepOptions}).Key(typed('b')); got.Step != StepVerification {
		t.Errorf("from step 3: Step = %v, want StepVerification", got.Step)
	}
	// On screen 1 there is nowhere to go back to, and 'b' is a character in a
	// server name like any other, so it is typed rather than obeyed.
	got := (State{Step: StepConnection}).Key(typed('b'))
	if got.Step != StepConnection {
		t.Errorf("from step 1: Step = %v, want StepConnection", got.Step)
	}
	if got.Server != "b" {
		t.Errorf("Server = %q, want the keystroke to have been typed", got.Server)
	}
}

func TestReProbeReturnsToTheWaitingStep(t *testing.T) {
	s := State{Step: StepVerification, Verify: collect.VerifyResult{Probed: true}}
	got := s.Key(typed('r'))
	// StepVerifying, not StepVerification: the probe is the slow thing, and
	// its screen is the one with the activity indicator.
	if got.Step != StepVerifying {
		t.Fatalf("Step = %v, want StepVerifying", got.Step)
	}
}

func TestWritingTheGrantScriptKeepsTheOperatorOnTheVerificationScreen(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 13, 14, 5, 0, 0, time.UTC)
	s := State{Step: StepVerification, Verify: verifyForGrants()}

	ok := s.writeGrantScript(dir, "0.18.0", now)
	if ok.Step != StepVerification {
		t.Fatalf("Step = %v, want StepVerification", ok.Step)
	}
	if ok.GrantPath == "" || ok.GrantError != nil {
		t.Fatalf("GrantPath = %q, GrantError = %v", ok.GrantPath, ok.GrantError)
	}

	// A failure is shown in full on the same screen: the operator can still
	// continue without the grant script, which is only an aid.
	bad := s
	bad.Verify.Probed = false
	failed := bad.writeGrantScript(dir, "0.18.0", now)
	if failed.Step != StepVerification {
		t.Fatalf("Step = %v after a failure, want StepVerification", failed.Step)
	}
	if failed.GrantError == nil {
		t.Error("GrantError is nil after a refused write")
	}
	if failed.GrantPath != "" {
		t.Errorf("GrantPath = %q after a failure, want empty", failed.GrantPath)
	}
}
