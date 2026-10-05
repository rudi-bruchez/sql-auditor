package collect

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// CostFlags are the opt-ins that are off by default for cost rather than for
// disclosure, and they are what "costly" means in the duration check prints.
//
// The definition is the one the tool already gave in prose, in three places
// that never shared a list: the README's table ("the two off for cost"), the
// comments on the two flags in main.go, and the wizard's third screen ("the
// last two only cost time"). It is not a threshold on @timeout. Most of the
// corpus sits above the 60-second default, so "longer than the default" would
// name forty collectors and warn about none; and the longest disclosure
// opt-in, --query-store-plan-stats at 600 seconds, is long because it reads
// the plan cache, not because it is a decision about spending the instance's
// I/O. What the operator can act on before a run is an option they passed,
// and these are the two whose only reason to be off is the time they take.
var CostFlags = map[string]bool{
	FlagEstimateCompression: true,
	FlagMeasurePageDensity:  true,
}

// unitTimeout is the limit runUnit gives a unit, and the name of the setting
// that fixed it for the message an expiry produces. It is one function because
// the ceiling check prints is only honest if it prices each unit at exactly
// the limit the run will apply.
func unitTimeout(s Script, cfg *Config) (time.Duration, string) {
	if s.TimeoutSec > 0 {
		return time.Duration(s.TimeoutSec) * time.Second, "@timeout"
	}
	return cfg.QueryTimeout, "SQL_QUERY_TIMEOUT_SEC"
}

// DurationBound is the longest the units of a planned run can take: every unit
// at its timeout, summed. It is a ceiling and never an estimate. The two
// collections of September 2026 this was written against spent 1 155 of 5 397
// seconds, and 1 022 of 1 923, in units that returned something, the rest in
// expiries: how long a unit runs depends on the instance, not on anything
// check can see, so the one figure that can be stated in advance and stay
// true is the limit.
//
// It does not bound the run itself. Connecting, the USE before each
// per-database unit, writing the files and the archive, and a reconnect after
// a dropped session all sit outside the unit timeouts.
type DurationBound struct {
	// Databases is the selection check lists, the same count as its
	// "Databases that would be collected" heading.
	Databases int
	// Costly is the collectors behind a CostFlags opt-in that will run, in
	// plan order, each named once however many databases it runs against.
	Costly        []string
	CostlyUnits   int
	CostlyCeiling time.Duration
	// Units and Ceiling cover the whole plan, costly units included.
	Units   int
	Ceiling time.Duration
}

// PlannedDuration prices the plan a run with this profile and these flags
// would execute on the verified instance. It unfolds that plan with planUnits,
// the function Run walks, so a per-database collector is counted once per
// database it will actually be pointed at, after the @writer narrowing and
// QUERY_STORE_DB_INCLUDE. It reports false when there is no plan to price: the
// probe failed, so the version gates and the denials are unknown, or the
// database list could not be built.
//
// The folders are the ones verification selected under the profile and flags
// it ran with. The wizard calls this again after the operator changes either,
// and a change that widens a different system database in is not reflected
// until the next verification; the database count is right, the units against
// that one database are not.
func PlannedDuration(v VerifyResult, profile string, flags map[string]bool) (DurationBound, bool) {
	if !v.Probed || v.CandidatesErr != nil || v.SelectErr != nil {
		return DurationBound{}, false
	}
	cfg := v.config
	if cfg == nil {
		cfg = &Config{}
	}
	units, _, _ := planUnits(planFor(v, profile, flags), v.Folders, cfg)
	b := DurationBound{Databases: len(v.Selection.Included), Units: len(units)}
	for _, u := range units {
		d, _ := unitTimeout(u.Script, cfg)
		b.Ceiling += d
		if !CostFlags[u.Script.RequiresFlag] {
			continue
		}
		b.CostlyUnits++
		b.CostlyCeiling += d
		if !slices.Contains(b.Costly, u.Script.Path) {
			b.Costly = append(b.Costly, u.Script.Path)
		}
	}
	return b, true
}

// Lines is the bound as check prints it and the wizard shows it. The word
// "ceiling" is in the heading the caller writes; every figure here carries its
// own condition, "if every one runs to its @timeout", so that a line copied
// out of its context cannot be read as a forecast.
//
// The costly part comes first and the whole plan second. The whole-plan
// figure is the only true ceiling on the units, but it is hours on any
// instance with a few databases, because each of eighty small collectors has a
// timeout too. The costly part is what an operator can change before the run.
func (b DurationBound) Lines() []string {
	dbs := fmt.Sprintf("%d databases", b.Databases)
	if b.Databases == 1 {
		dbs = "1 database"
	}
	var out []string
	if len(b.Costly) == 0 {
		var opts []string
		for f := range CostFlags {
			opts = append(opts, flagOption(f))
		}
		slices.Sort(opts)
		out = append(out, fmt.Sprintf("%s; no costly collector on (%s are off)", dbs, strings.Join(opts, ", ")))
	} else {
		out = append(out,
			fmt.Sprintf("%s; costly collectors on: %s;", dbs, strings.Join(b.Costly, ", ")),
			fmt.Sprintf("  at most %s if every one of their %d units runs to its @timeout",
				formatCeiling(b.CostlyCeiling), b.CostlyUnits))
	}
	return append(out, fmt.Sprintf("all %d units: at most %s if every one runs to its @timeout",
		b.Units, formatCeiling(b.Ceiling)))
}

// formatCeiling writes hours and minutes for a reader planning a window, and
// the seconds beside them for one comparing against duration_sec in _run.json.
func formatCeiling(d time.Duration) string {
	s := int64(d / time.Second)
	var human string
	switch {
	case d >= time.Hour:
		human = fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
	default:
		human = fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%s (%d s)", human, s)
}

// BoundLine is the bound as check and the wizard's third screen state it:
// the value written by formatCeiling, since Config keeps no spelling and
// time.Duration prints 2h as 2h0m0s; the key's name as fixed text, so the
// operator learns what to look for in .env; the provenance, because a .env
// beats an exported variable here and a bound nobody remembers setting is
// the one that will be argued about; and the rule. Empty without a bound.
func BoundLine(cfg *Config) string {
	if cfg == nil || cfg.MaxDuration <= 0 {
		return ""
	}
	return fmt.Sprintf("MAX_DURATION at %s, from %s; no collector starts after it, and the one running then is stopped",
		formatCeiling(cfg.MaxDuration), cfg.MaxDurationFrom)
}

// Against compares the ceiling with the bound and makes no forecast. When
// costly collectors are on and the whole ceiling is above the bound, it gives
// the ceiling of the units before them, since those can exceed the bound too
// and a line that named only the costly ones would mislead. It never promises
// that only costly collectors will be cut: the bound also counts the steps
// before the first unit and the resets between units, which no ceiling does.
func (b DurationBound) Against(limit time.Duration) string {
	head := "bounded at " + formatCeiling(limit) + ": "
	switch {
	case b.Ceiling <= limit:
		return head + "the ceiling of the units is under the bound"
	case len(b.Costly) > 0:
		before := b.Ceiling - b.CostlyCeiling
		side := "above"
		if before <= limit {
			side = "under"
		}
		return fmt.Sprintf("%sthe costly collectors run last; the %d units before them: at most %s, %s the bound",
			head, b.Units-b.CostlyUnits, formatCeiling(before), side)
	default:
		return head + "the ceiling is above the bound; if it is reached, the collectors last in the plan are the ones not run"
	}
}
