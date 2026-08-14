package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestElapsedIsWrittenAtTheScaleItHasReached(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{9 * time.Second, "9s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m00s"},
		{134 * time.Second, "2m14s"},
		{59*time.Minute + 59*time.Second, "59m59s"},
		{time.Hour, "1h00m"},
		{3*time.Hour + 7*time.Minute + 30*time.Second, "3h07m"},
	}
	for _, c := range cases {
		// Seconds are dropped past the hour on purpose. A run in its fourth
		// hour is watched for whether it is still moving, not timed to the
		// second, and the column is worth more to the collector's name.
		if got := elapsed(c.in); got != c.want {
			t.Errorf("elapsed(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGaugeShowsTheCountThePercentTheClockAndTheUnit(t *testing.T) {
	got := gauge(47, 223, 134*time.Second, "10.system/052.session-text.sql (CLIENTDB)", 100)
	for _, want := range []string{"47/223", "21%", "2m14s", "10.system/052.session-text.sql", "CLIENTDB"} {
		if !strings.Contains(got, want) {
			t.Errorf("gauge = %q, missing %q", got, want)
		}
	}
	// The counter is padded to the width of the denominator, so the columns
	// after it do not jump left and right as the numerator gains digits.
	if !strings.Contains(got, "[ 47/223]") {
		t.Errorf("gauge = %q, want the numerator right-aligned under the denominator", got)
	}
}

// The plan is not known until planUnits has run, and the phases after the last
// unit have no denominator at all. Both must still produce a line, or the
// screen goes blank exactly when the run is doing something slow and unnamed.
func TestGaugeWithoutADenominatorShowsTheClockAndTheLabel(t *testing.T) {
	got := gauge(0, 0, 3*time.Second, "archiving", 80)
	if strings.Contains(got, "%") {
		t.Errorf("gauge = %q, want no percentage when nothing is planned", got)
	}
	for _, want := range []string{"3s", "archiving"} {
		if !strings.Contains(got, want) {
			t.Errorf("gauge = %q, missing %q", got, want)
		}
	}
}

// The line is rewritten in place, so one that wrapped would leave its tail
// behind on the screen at every repaint. It is cut to one column short of the
// width: the last cell is where the cursor sits.
func TestGaugeNeverFillsTheLastColumn(t *testing.T) {
	long := "40.database/210.some-collector-with-a-very-long-name.sql (A_DATABASE_WITH_A_LONG_NAME)"
	for _, width := range []int{20, 40, 79, 80, 200} {
		got := gauge(7, 9, time.Second, long, width)
		if len(got) > width-1 {
			t.Errorf("width %d: gauge is %d columns, want at most %d", width, len(got), width-1)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("width %d: gauge carries its own line ending: %q", width, got)
		}
	}
}

// A width of zero is what GetSize reports while an RDP window is being dragged.
// It must not produce a negative slice bound.
func TestGaugeSurvivesAnUnknownWidth(t *testing.T) {
	if got := gauge(1, 2, time.Second, "x", 0); got == "" {
		t.Error("a zero width produced no line at all")
	}
}

// Redirected, the gauge is not a gauge: `2> run.log` has to stay a file a
// person can read, so each finished unit gets one plain line and nothing is
// rewritten.
func TestWithoutATerminalEachUnitGetsItsOwnPlainLine(t *testing.T) {
	var b strings.Builder
	o := newProgress(&b, false, func() int { return 80 }, fixedClock())
	o.Planned(2)
	o.UnitStarted("10.system/001.a.sql", "")
	o.UnitDone("10.system/001.a.sql", "", 2048, 300*time.Millisecond, nil)
	o.UnitStarted("40.database/210.b.sql", "CLIENTDB")
	o.UnitDone("40.database/210.b.sql", "CLIENTDB", 100, time.Second, nil)
	o.Finished(false)

	out := b.String()
	if strings.Contains(out, "\r") {
		t.Errorf("a redirected stream carries carriage returns:\n%q", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per unit:\n%q", len(lines), out)
	}
	if !strings.Contains(lines[0], "1/2") || !strings.Contains(lines[0], "10.system/001.a.sql") {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "CLIENTDB") {
		t.Errorf("second line = %q", lines[1])
	}
}

// A failed unit is the one thing that must survive on screen. Everything else
// the gauge says is replaced a second later; a failure is a fact about this run
// that the operator cannot get back once the line is overwritten.
func TestAFailedUnitLeavesAPermanentLine(t *testing.T) {
	for _, tty := range []bool{true, false} {
		var b strings.Builder
		o := newProgress(&b, tty, func() int { return 80 }, fixedClock())
		o.Planned(1)
		o.UnitStarted("40.database/210.b.sql", "CLIENTDB")
		o.UnitDone("40.database/210.b.sql", "CLIENTDB", 0, time.Second, errors.New("timeout expired"))
		o.Finished(false)

		out := b.String()
		// The permanent part is what remains once the transient line and its
		// erasures are cut away at the newlines.
		var kept []string
		for _, l := range strings.Split(out, "\n") {
			if i := strings.LastIndex(l, "\r"); i >= 0 {
				l = l[i+1:]
			}
			if strings.TrimSpace(l) != "" {
				kept = append(kept, strings.TrimSpace(l))
			}
		}
		if len(kept) == 0 {
			t.Fatalf("tty=%v: nothing survived:\n%q", tty, out)
		}
		joined := strings.Join(kept, "\n")
		for _, want := range []string{"210.b.sql", "CLIENTDB", "timeout expired"} {
			if !strings.Contains(joined, want) {
				t.Errorf("tty=%v: what stayed on screen is missing %q:\n%s", tty, want, joined)
			}
		}
	}
}

// On a terminal the line is rewritten, never appended: a run of 223 units must
// not leave 223 lines of scrollback behind the archive path.
func TestOnATerminalTheGaugeRewritesOneLine(t *testing.T) {
	var b strings.Builder
	o := newProgress(&b, true, func() int { return 80 }, fixedClock())
	o.Planned(3)
	for _, s := range []string{"a.sql", "b.sql", "c.sql"} {
		o.UnitStarted(s, "")
		o.UnitDone(s, "", 10, time.Second, nil)
	}
	o.Finished(false)

	out := b.String()
	if strings.Count(out, "\n") != 0 {
		t.Errorf("the gauge broke the line %d times:\n%q", strings.Count(out, "\n"), out)
	}
	if !strings.Contains(out, "\r") {
		t.Errorf("nothing was rewritten in place:\n%q", out)
	}
	// And it leaves the line clean, so the summary that collect prints to
	// stdout does not land on top of a half-erased gauge.
	if !strings.HasSuffix(out, "\r\x1b[K") {
		t.Errorf("the last thing written does not erase the line: %q", out[max(0, len(out)-40):])
	}
}

// Skips are not shown. A run narrowed by QUERY_STORE_DB_INCLUDE skips one
// script per database, and a screen that named each of them would push the
// thing being waited on off the top.
func TestSkipsAreNotPrinted(t *testing.T) {
	var b strings.Builder
	o := newProgress(&b, false, func() int { return 80 }, fixedClock())
	o.Planned(1)
	o.ScriptSkipped("40.database/022.query-store-detail.sql", "OTHERDB", "not selected")
	o.Finished(false)
	if strings.Contains(b.String(), "OTHERDB") {
		t.Errorf("a skip was printed:\n%q", b.String())
	}
}

// fixedClock advances one second per call, which is enough for the elapsed
// column to be non-zero without any test depending on real time.
func fixedClock() func() time.Time {
	base := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
}
