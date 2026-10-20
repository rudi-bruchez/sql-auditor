package host

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// EventQuery is one Event Log query of the host read, run against one channel.
type EventQuery struct {
	ID        string // "1", "2", "2c", "3a", "3b", "4"
	Channel   string // "System", "Application", "Microsoft-Windows-FailoverClustering/Operational"
	XPath     string
	Cap       int  // 0: no cap
	CountOnly bool // query 2c: count per id, keep nothing
}

// eventCap is the number of events kept by a capped query, newest first.
const eventCap = 20000

// ClusterChannel is the failover cluster's operational channel, read by query 4.
const ClusterChannel = "Microsoft-Windows-FailoverClustering/Operational"

// BuildEventQueries returns the queries of the window starting at since. With
// source "" (the instance's event source is undecided), queries 2, 2c and 3b
// are left out and the caller records them as not run.
func BuildEventQueries(source string, since time.Time) []EventQuery {
	tm := "TimeCreated[@SystemTime>='" + since.UTC().Format("2006-01-02T15:04:05.000Z") + "']"
	prov := "Provider[@Name='" + source + "']"
	q1 := "*[System[(Level=1 or Level=2 or Level=3) and " + tm + "]]"
	qs := []EventQuery{
		{ID: "1", Channel: "System", XPath: q1, Cap: eventCap},
		{ID: "1", Channel: "Application", XPath: q1, Cap: eventCap},
	}
	if source != "" {
		// Query 2 leaves out 3b's identifiers, so a restart is never counted
		// twice against its cap, and 2c's, which are counted rather than kept.
		qs = append(qs,
			EventQuery{ID: "2", Channel: "Application", XPath: "*[System[" + prov + " and " + tm + " and (EventID<41050 or EventID>41055) and EventID!=17162 and EventID!=17147 and EventID!=17148]]", Cap: eventCap},
			EventQuery{ID: "2c", Channel: "Application", XPath: "*[System[" + prov + " and " + tm + " and EventID>=41050 and EventID<=41055]]", CountOnly: true})
	}
	qs = append(qs, EventQuery{ID: "3a", Channel: "System", XPath: "*[System[" + tm + " and ((Provider[@Name='User32'] and (EventID=1074 or EventID=1076)) or (Provider[@Name='EventLog'] and (EventID=6005 or EventID=6006 or EventID=6008)) or (Provider[@Name='Microsoft-Windows-Kernel-Power'] and EventID=41))]]", Cap: eventCap})
	if source != "" {
		qs = append(qs, EventQuery{ID: "3b", Channel: "Application", XPath: "*[System[" + prov + " and " + tm + " and (EventID=17162 or EventID=17147 or EventID=17148)]]", Cap: eventCap})
	}
	return append(qs, EventQuery{ID: "4", Channel: ClusterChannel, XPath: "*[System[" + tm + "]]", Cap: eventCap})
}

// Event is one event record as the host read writes it.
type Event struct {
	Log      string   `json:"log"`
	Queries  []string `json:"queries"`
	TimeUTC  string   `json:"time_utc"` // RFC 3339, from System/TimeCreated/@SystemTime
	Level    int      `json:"level"`
	Provider string   `json:"provider"`
	EventID  int      `json:"event_id"`
	RecordID uint64   `json:"record_id"`
	Data     []string `json:"data"`
	Binary   string   `json:"binary,omitempty"` // hex
	Message  string   `json:"message"`
	// Cut is true when Keep cut the message, a Data value or Binary.
	Cut bool `json:"cut,omitempty"`
	// DataDropped is how many Data values, at the end, Keep left out to stay
	// within dataBytes.
	DataDropped int `json:"data_dropped,omitempty"`
}

// timeLayout keeps the seven fractional digits of SystemTime, always written,
// so that two TimeUTC values compare as strings in time order.
const timeLayout = "2006-01-02T15:04:05.0000000Z"

type eventXML struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/win/2004/08/events/event Event"`
	System  struct {
		Provider struct {
			Name string `xml:"Name,attr"`
		} `xml:"Provider"`
		EventID     int `xml:"EventID"` // its Qualifiers attribute is not part of the id
		Level       int `xml:"Level"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
		EventRecordID uint64 `xml:"EventRecordID"`
		Channel       string `xml:"Channel"`
	} `xml:"System"`
	EventData struct {
		Inner  []byte `xml:",innerxml"`
		Binary string `xml:"Binary"`
	} `xml:"EventData"`
	UserData struct {
		Inner []byte `xml:",innerxml"`
	} `xml:"UserData"`
}

// ParseEventXML reads an event as EvtRender(EvtRenderEventXml) returns it.
// Data holds the text of every Data element under EventData, at any depth and
// in document order, or the text of UserData's leaf elements in document order;
// it is empty, never nil, when the event has no payload. A character outside
// the XML 1.0 range, as a reference or a raw byte, is kept as its \xNN text.
// Queries and Message are left to the caller.
//
// ponytail: UserData attribute-only leaves and mixed content are not read; add
// them when an event of interest carries its payload there.
func ParseEventXML(b []byte) (Event, error) {
	var x eventXML
	if err := xml.Unmarshal(escapeControl(b), &x); err != nil {
		return Event{}, fmt.Errorf("event XML: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, x.System.TimeCreated.SystemTime)
	if err != nil {
		return Event{}, fmt.Errorf("event XML: TimeCreated: %w", err)
	}
	e := Event{
		Log:      x.System.Channel,
		TimeUTC:  t.UTC().Format(timeLayout),
		Level:    x.System.Level,
		Provider: x.System.Provider.Name,
		EventID:  x.System.EventID,
		RecordID: x.System.EventRecordID,
		Binary:   x.EventData.Binary,
	}
	if len(x.UserData.Inner) > 0 {
		e.Data, err = leafTexts(x.UserData.Inner)
	} else {
		e.Data, err = dataTexts(x.EventData.Inner)
	}
	if err != nil {
		return Event{}, fmt.Errorf("event XML: payload: %w", err)
	}
	if e.Data == nil {
		e.Data = []string{}
	}
	return e, nil
}

var charRef = regexp.MustCompile(`&#[xX]([0-9a-fA-F]+);|&#([0-9]+);`)

// escapeControl replaces the character references and the raw bytes that XML 1.0
// forbids with their \xNN text, so that the value stays visible.
func escapeControl(b []byte) []byte {
	b = charRef.ReplaceAllFunc(b, func(m []byte) []byte {
		s, base := string(m[2:len(m)-1]), 10
		if s[0] == 'x' || s[0] == 'X' {
			s, base = s[1:], 16
		}
		n, err := strconv.ParseUint(s, base, 32)
		if err == nil && validXMLChar(rune(n)) {
			return m
		}
		return []byte(escaped(n))
	})
	var out []byte
	for i, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			if out == nil {
				out = append(make([]byte, 0, len(b)+8), b[:i]...)
			}
			out = append(out, escaped(uint64(c))...)
		} else if out != nil {
			out = append(out, c)
		}
	}
	if out == nil {
		return b
	}
	return out
}

func validXMLChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF
}

// escaped is the \xNN text of a character, or \u{N} past one byte.
func escaped(n uint64) string {
	if n < 0x100 {
		return fmt.Sprintf(`\x%02X`, n)
	}
	return fmt.Sprintf(`\u{%X}`, n)
}

// dataTexts returns the text of every Data element at any depth, in order.
func dataTexts(b []byte) ([]string, error) {
	var out []string
	var text strings.Builder
	in := false
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if tok.Name.Local == "Data" {
				in = true
				text.Reset()
			}
		case xml.CharData:
			if in {
				text.Write(tok)
			}
		case xml.EndElement:
			if in && tok.Name.Local == "Data" {
				out = append(out, text.String())
				in = false
			}
		}
	}
}

// leafTexts returns the text of every element without child elements.
func leafTexts(b []byte) ([]string, error) {
	var out []string
	var text strings.Builder
	leaf := false
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			leaf = true
			text.Reset()
		case xml.CharData:
			text.Write(tok)
		case xml.EndElement:
			if leaf {
				out = append(out, text.String())
			}
			leaf = false
		}
	}
}

// messageRunes is where a rendered message, a Data value or Binary is cut.
const messageRunes = 2000

// dataBytes is the most of Data text kept per event, the values cut first:
// 20,000 events of 30 KB of Data each held 4.3 GB of the server's memory, and
// 16 KiB still left about 1.5 GB of heap for two capped queries (harm review,
// batch D).
const dataBytes = 4 << 10

// dataValueOverhead is what each Data value is charged against dataBytes on
// top of its text, its string header: thousands of empty values otherwise
// cost nothing, and 2,000 events of 5,000 empty values held 157 MiB live, 10.5
// MiB once charged (harm review, batch E). Any local user can write such an
// event with ReportEvent.
const dataValueOverhead = 16

// CutMessage returns s cut at 2,000 characters, counted in runes.
func CutMessage(s string) string {
	s, _ = cut(s)
	return s
}

// cut is s cut at messageRunes, a copy when cut so that s is not kept alive,
// and whether it was.
func cut(s string) (string, bool) {
	n := 0
	for i := range s {
		if n == messageRunes {
			return strings.Clone(s[:i]), true
		}
		n++
	}
	return s, false
}

// Keep sets e's message and bounds what e holds: the message, each Data value
// and Binary cut at messageRunes, Data past dataBytes in all, each value
// charged dataValueOverhead too, left out.
func (e *Event) Keep(message string) {
	var c bool
	e.Message, c = cut(message)
	e.Cut = e.Cut || c
	e.Binary, c = cut(e.Binary)
	e.Cut = e.Cut || c
	total, n := 0, len(e.Data)
	for i, v := range e.Data {
		v, c = cut(v)
		e.Cut = e.Cut || c
		if total += len(v) + dataValueOverhead; total > dataBytes {
			n, e.DataDropped = i, len(e.Data)-i
			break
		}
		if !c {
			v = strings.Clone(v) // a strings.Builder's String keeps its spare capacity
		}
		e.Data[i] = v
	}
	// A new array: the parser's, as long as every value and grown by append,
	// would stay alive with the event.
	e.Data = slices.Clone(e.Data[:n])
}

// IDCount is how many events of one id query 2c met, and when.
type IDCount struct {
	EventID  int    `json:"event_id"`
	Count    int    `json:"count"`
	FirstUTC string `json:"first_utc"`
	LastUTC  string `json:"last_utc"`
}

// Counter counts events per id, in whatever order they come.
type Counter struct{ m map[int]*IDCount }

// Add counts one event; timeUTC is an Event.TimeUTC.
func (c *Counter) Add(id int, timeUTC string) {
	if c.m == nil {
		c.m = map[int]*IDCount{}
	}
	n, ok := c.m[id]
	if !ok {
		c.m[id] = &IDCount{EventID: id, Count: 1, FirstUTC: timeUTC, LastUTC: timeUTC}
		return
	}
	n.Count++
	if timeUTC < n.FirstUTC {
		n.FirstUTC = timeUTC
	}
	if timeUTC > n.LastUTC {
		n.LastUTC = timeUTC
	}
}

// Counts returns the counts by EventID ascending.
func (c *Counter) Counts() []IDCount {
	out := make([]IDCount, 0, len(c.m))
	for _, n := range c.m {
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EventID < out[j].EventID })
	return out
}

// Coverage is what one log or channel holds, so that "no event" is not read
// as "log too short".
type Coverage struct {
	OldestUTC    string `json:"oldest_utc"`
	MaxSizeBytes uint64 `json:"max_size_bytes"`
	Retention    bool   `json:"retention"`
	AutoBackup   bool   `json:"auto_backup"`
}

// EventsFile is the content of one events JSON file of the archive.
type EventsFile struct {
	Log       string            `json:"log"`
	Source    string            `json:"instance_source,omitempty"`
	Coverage  Coverage          `json:"coverage"`
	Events    []Event           `json:"events"`
	Counted   []IDCount         `json:"counted,omitempty"`
	NotRun    map[string]string `json:"not_run,omitempty"`              // query id -> reason
	Truncated map[string]string `json:"truncated_before_utc,omitempty"` // query id -> oldest time kept; per query, since queries 1 and 2 share the Application file
}

// Merge returns one event per (Log, RecordID), with the queries that matched
// it unioned and sorted, newest first.
func Merge(lists ...[]Event) []Event {
	type key struct {
		log string
		rec uint64
	}
	at := map[key]int{}
	var out []Event
	for _, l := range lists {
		for _, e := range l {
			k := key{e.Log, e.RecordID}
			i, ok := at[k]
			if !ok {
				at[k] = len(out)
				e.Queries = append([]string(nil), e.Queries...)
				out = append(out, e)
				continue
			}
			for _, q := range e.Queries {
				if !slices.Contains(out[i].Queries, q) {
					out[i].Queries = append(out[i].Queries, q)
				}
			}
		}
	}
	for i := range out {
		sort.Strings(out[i].Queries)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeUTC != out[j].TimeUTC {
			return out[i].TimeUTC > out[j].TimeUTC
		}
		return out[i].RecordID > out[j].RecordID
	})
	return out
}
