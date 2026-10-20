package host

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildEventQueriesExactText(t *testing.T) {
	since := time.Date(2026, 7, 22, 0, 0, 0, 0, time.FixedZone("x", -7*3600))
	tm := "TimeCreated[@SystemTime>='2026-07-22T07:00:00.000Z']"
	src := "MSSQL$PROD"
	want := []EventQuery{
		{ID: "1", Channel: "System", XPath: "*[System[(Level=1 or Level=2 or Level=3) and " + tm + "]]", Cap: 20000},
		{ID: "1", Channel: "Application", XPath: "*[System[(Level=1 or Level=2 or Level=3) and " + tm + "]]", Cap: 20000},
		{ID: "2", Channel: "Application", XPath: "*[System[Provider[@Name='" + src + "'] and " + tm + " and (EventID<41050 or EventID>41055) and EventID!=17162 and EventID!=17147 and EventID!=17148]]", Cap: 20000},
		{ID: "2c", Channel: "Application", XPath: "*[System[Provider[@Name='" + src + "'] and " + tm + " and EventID>=41050 and EventID<=41055]]", CountOnly: true},
		{ID: "3a", Channel: "System", XPath: "*[System[" + tm + " and ((Provider[@Name='User32'] and (EventID=1074 or EventID=1076)) or (Provider[@Name='EventLog'] and (EventID=6005 or EventID=6006 or EventID=6008)) or (Provider[@Name='Microsoft-Windows-Kernel-Power'] and EventID=41))]]"},
		{ID: "3b", Channel: "Application", XPath: "*[System[Provider[@Name='" + src + "'] and " + tm + " and (EventID=17162 or EventID=17147 or EventID=17148)]]"},
		{ID: "4", Channel: "Microsoft-Windows-FailoverClustering/Operational", XPath: "*[System[" + tm + "]]", Cap: 20000},
	}
	got := BuildEventQueries(src, since)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildEventQueries:\n got %#v\nwant %#v", got, want)
	}
}

func TestBuildEventQueriesWithoutSource(t *testing.T) {
	var ids []string
	for _, q := range BuildEventQueries("", time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)) {
		ids = append(ids, q.ID)
	}
	if want := []string{"1", "1", "3a", "4"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

func readEvent(t *testing.T, name string) Event {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	e, err := ParseEventXML(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestParseEventXML18264(t *testing.T) {
	got := readEvent(t, "event-18264.xml")
	want := Event{
		Log:      "Application",
		TimeUTC:  "2026-10-20T01:50:58.1234567Z",
		Level:    4,
		Provider: "MSSQLSERVER",
		EventID:  18264,
		RecordID: 12345,
		Data:     []string{"SALESDB", "2025/11/21", "05:36:04", "595"},
		Binary:   "58470000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseEventXML:\n got %#v\nwant %#v", got, want)
	}
}

func TestParseEventXMLUserData(t *testing.T) {
	if got := readEvent(t, "event-userdata.xml").Data; !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("Data = %q, want [x y]", got)
	}
}

func TestMergeUnionsQueries(t *testing.T) {
	older := Event{Log: "Application", RecordID: 1, TimeUTC: "2026-10-19T00:00:00.0000000Z", Queries: []string{"1"}}
	e1 := Event{Log: "Application", RecordID: 2, TimeUTC: "2026-10-20T00:00:00.0000000Z", Queries: []string{"1"}}
	e2 := e1
	e2.Queries = []string{"2"}
	got := Merge([]Event{older, e1}, []Event{e2})
	if len(got) != 2 || got[0].RecordID != 2 || got[1].RecordID != 1 {
		t.Fatalf("Merge = %+v, want records 2 then 1", got)
	}
	if !reflect.DeepEqual(got[0].Queries, []string{"1", "2"}) {
		t.Errorf("Queries = %v, want [1 2]", got[0].Queries)
	}
}

func TestCutMessage(t *testing.T) {
	if got := CutMessage(strings.Repeat("é", 2001)); got != strings.Repeat("é", 2000) {
		t.Errorf("CutMessage kept %d runes, want 2000", len([]rune(got)))
	}
}

func TestCounter(t *testing.T) {
	t1, t2, t3 := "2026-10-01T00:00:00.0000000Z", "2026-10-02T00:00:00.0000000Z", "2026-10-03T00:00:00.0000000Z"
	want := []IDCount{{41050, 1, t1, t1}, {41051, 2, t2, t3}}
	for _, order := range [][]struct {
		id int
		t  string
	}{
		{{41051, t3}, {41051, t2}, {41050, t1}},
		{{41051, t2}, {41050, t1}, {41051, t3}},
	} {
		var c Counter
		for _, a := range order {
			c.Add(a.id, a.t)
		}
		if got := c.Counts(); !reflect.DeepEqual(got, want) {
			t.Errorf("Counts = %v, want %v", got, want)
		}
	}
}
