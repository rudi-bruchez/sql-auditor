package tui

import (
	"testing"

	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

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
