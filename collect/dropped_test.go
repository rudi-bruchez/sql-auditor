package collect

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

// Error 911 is what USE answers for a database that is not there, and it is
// not proof by itself: the number says the name did not resolve for this
// session, and only the catalog says the database is gone. A 911 on a
// database that still exists stays an error.
func TestADroppedDatabaseIsConfirmedByTheCatalog(t *testing.T) {
	notThere := mssql.Error{Number: 911, Message: "Database 'TEMPETL' does not exist. Make sure that the name is entered correctly."}
	other := mssql.Error{Number: 208, Message: "Invalid object name 'sys.nothing'."}
	gone := func(string) (bool, error) { return false, nil }
	present := func(string) (bool, error) { return true, nil }
	unknown := func(string) (bool, error) { return false, errors.New("the check timed out") }
	cases := []struct {
		name   string
		err    error
		target string
		exists func(string) (bool, error)
		want   bool
	}{
		{"911 on a database the catalog no longer lists", notThere, "TEMPETL", gone, true},
		{"911 wrapped on its way up", fmt.Errorf("unit: %w", notThere), "TEMPETL", gone, true},
		{"911 on a database that still exists", notThere, "TEMPETL", present, false},
		{"911 and the catalog could not be asked", notThere, "TEMPETL", unknown, false},
		{"another error on a database that is gone", other, "TEMPETL", gone, false},
		{"an error with no number on a database that is gone", errors.New("i/o timeout"), "TEMPETL", gone, false},
		{"911 on an instance-scope unit", notThere, "", gone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := databaseDropped(c.err, c.target, c.exists); got != c.want {
				t.Errorf("databaseDropped = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDroppedSkipsOnlyTheDroppedDatabase(t *testing.T) {
	on := map[string]bool{"TEMPETL": true}
	if r, ok := droppedBefore(on, "TEMPETL"); !ok || r != skipDroppedDuringRun {
		t.Errorf("TEMPETL: %q, %v", r, ok)
	}
	if _, ok := droppedBefore(on, "SALESDB"); ok {
		t.Errorf("another database was skipped")
	}
	on[""] = true
	if _, ok := droppedBefore(on, ""); ok {
		t.Errorf("an instance-scope unit was skipped")
	}
}

// The unit that met the missing database is a skip like the ones after it,
// and the disappearance is told once, however many collectors it took.
func TestADroppedDatabaseIsRecordedOnceAndAsASkip(t *testing.T) {
	m := NewManifest("sql-auditor", "0.19.0", "abc1234")
	on := map[string]bool{}
	err := mssql.Error{Number: 911, Message: "Database 'TEMPETL' does not exist."}
	report := noteDropped(m, on, "20.databases/020.properties.sql", "TEMPETL", err)
	var skip *UnitSkipped
	if !errors.As(report, &skip) || skip.Reason != skipDroppedDuringRun {
		t.Errorf("the screen would be told %v, want a skip", report)
	}
	if len(m.Errors) != 0 {
		t.Errorf("errors %v, want none", m.Errors)
	}
	if len(m.Skipped) != 1 || m.Skipped[0].Target != "TEMPETL" || m.Skipped[0].Reason != skipDroppedDuringRun {
		t.Errorf("skipped %v, want the unit on TEMPETL", m.Skipped)
	}
	if _, ok := droppedBefore(on, "TEMPETL"); !ok {
		t.Errorf("the next unit on TEMPETL would run")
	}
	told := 0
	for _, w := range m.Warnings {
		if strings.Contains(w, "TEMPETL") {
			told++
			if !strings.Contains(w, "dropped during the collection") || !strings.Contains(w, "020.properties") {
				t.Errorf("the warning does not say what happened: %q", w)
			}
		}
	}
	if told != 1 {
		t.Errorf("%d warning(s) name TEMPETL, want 1: %v", told, m.Warnings)
	}
}

// MANIFEST.txt names the database once rather than repeating the reason under
// every collector it took; _run.json keeps each skip.
func TestManifestHumanGroupsTheSkipsOfADroppedDatabase(t *testing.T) {
	m := &Manifest{}
	m.Skipped = []SkippedScript{
		{Script: "20.databases/020.properties.sql", Target: "TEMPETL", Reason: skipDroppedDuringRun},
		{Script: "20.databases/030.files.sql", Target: "TEMPETL", Reason: skipDroppedDuringRun},
		{Script: "70.schema/041.compression-savings.sql", Reason: "not collected by default; pass --estimate-compression to include it"},
	}
	h := m.Human()
	if !strings.Contains(h, "Queries not run (3):") {
		t.Errorf("the heading must count every entry of skipped_scripts:\n%s", h)
	}
	if !strings.Contains(h, "  - 2 collectors on TEMPETL, each listed in _run.json\n      "+skipDroppedDuringRun) {
		t.Errorf("the skips of the dropped database must collapse into one entry:\n%s", h)
	}
	if strings.Contains(h, "030.files.sql") {
		t.Errorf("a collapsed skip must not also be listed:\n%s", h)
	}
	if !strings.Contains(h, "70.schema/041.compression-savings.sql") {
		t.Errorf("a skip for another reason must still be listed:\n%s", h)
	}
}
