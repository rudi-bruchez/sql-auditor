package host

import (
	"strings"
	"testing"
	"time"
)

func TestParseSinceDefaultIsNinetyDaysAtLocalMidnight(t *testing.T) {
	loc := time.FixedZone("x", -7*3600)
	now := time.Date(2026, 10, 20, 15, 4, 5, 0, loc)
	got, err := ParseSince("", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 22, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// Across a daylight saving change, 90 calendar days and 90 times 24 hours
	// differ by an hour: only the first lands on local midnight.
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("no zone database:", err)
	}
	got, err = ParseSince("", time.Date(2026, 11, 15, 10, 0, 0, 0, paris))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 8, 17, 0, 0, 0, 0, paris); !got.Equal(want) {
		t.Errorf("across DST: got %v, want %v", got, want)
	}
}

func TestParseSinceReadsLocalDate(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	now := time.Date(2026, 10, 20, 15, 4, 5, 0, loc)
	got, err := ParseSince("2026-09-01", now)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseSinceRefusesOtherShapes(t *testing.T) {
	now := time.Date(2026, 10, 20, 15, 4, 5, 0, time.UTC)
	for _, s := range []string{"2026-9-1", "01/09/2026", "2026-09-01T00:00"} {
		_, err := ParseSince(s, now)
		if err == nil {
			t.Errorf("%q: accepted", s)
			continue
		}
		if !strings.Contains(err.Error(), "yyyy-mm-dd") {
			t.Errorf("%q: error %q does not name yyyy-mm-dd", s, err)
		}
	}
}

func TestParseSinceRefusesTheFuture(t *testing.T) {
	now := time.Date(2026, 10, 20, 15, 4, 5, 0, time.UTC)
	if _, err := ParseSince("2026-10-21", now); err == nil {
		t.Error("tomorrow accepted")
	}
	if _, err := ParseSince("2026-10-20", now); err != nil {
		t.Errorf("today refused: %v", err)
	}
}
