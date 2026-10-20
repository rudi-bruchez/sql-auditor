package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// ErrChannelNotFound is ERROR_EVT_CHANNEL_NOT_FOUND (15007): the channel does
// not exist on this machine, as the cluster's on a host that is not a node.
var ErrChannelNotFound = errors.New("event channel not found")

// Calls are the Windows primitives the sources read through, so that every
// rule of the sources runs, and is tested, away from Windows.
type Calls struct {
	// Query runs an XPath query on a channel, newest first, giving each event
	// as EvtRender XML with its EvtFormatMessage text, "" when the publisher's
	// message file is gone or format is false. each returns false to stop.
	Query      func(ctx context.Context, channel, xpath string, format bool, each func(eventXML []byte, message string) bool) error
	Coverage   func(channel string) (Coverage, error)
	PowerShell func(ctx context.Context, script string) ([]byte, error)
	Filters    func() ([]Filter, error)
	Power      func() (PowerPlan, error)
	OpenDir    func(dir string) (fs.FS, error)
	ReadFile   func(path string) ([]byte, error)
	// DirCheck judges a directory as typed, before OpenDir lists it: listing a
	// network directory already sends the account's credentials to it.
	DirCheck PathCheck
	// OpenFile opens every file the sources copy.
	OpenFile OpenFile

	ProgramFiles, AzurePlugins string

	capOverride int // tests only: replaces every non-zero Cap
}

// extensionBudget is the most of the VM extension logs copied.
const extensionBudget = 50 << 20

// sqlWriterTail is the most copied of each SQL Writer log, its newest bytes.
const sqlWriterTail = 10 << 20

// sqlWriterBudget is the most copied of all the SQL Writer logs together,
// newest first: anyone who can write in the TraceFile's directory can add
// files named after its stem.
const sqlWriterBudget = 50 << 20

// BuildSources returns the sources in archive order.
func BuildSources(c Calls) []Source {
	events := func(channel, file string) func(context.Context, Env, FileSink) SourceResult {
		return func(ctx context.Context, e Env, put FileSink) SourceResult {
			return c.readEvents(ctx, e, put, channel, file)
		}
	}
	return []Source{
		{Name: "010.events-system", Read: events("System", "010.events-system.json")},
		{Name: "020.events-application", Read: events("Application", "020.events-application.json")},
		{Name: "025.events-cluster", Read: events(ClusterChannel, "025.events-cluster.json")},
		{Name: "030.vss", Read: c.readVSS},
		{Name: "035.sqlwriter", Read: c.readSQLWriter},
		{Name: "040.filter-drivers", Read: c.readFilters},
		{Name: "050.power-plan", Read: c.readPower},
		{Name: "060.vm-extensions", Read: c.readExtensions},
		{Name: "070.job-outputs", Read: c.readJobOutputs},
		{Name: "080.cluster", Read: c.readCluster},
	}
}

func failed(err error) SourceResult { return SourceResult{Status: StatusFailed, Reason: err.Error()} }

// written writes v as the source's JSON file and returns it read, or failed.
func written(put FileSink, file string, v any, count int) SourceResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		err = put(file, bytes.NewReader(b))
	}
	if err != nil {
		return failed(err)
	}
	return SourceResult{Status: StatusRead, Count: count}
}

// readEvents runs every query of one channel into one file. An event whose
// XML does not parse is left out and counted in a note rather than costing
// the log.
func (c Calls) readEvents(ctx context.Context, e Env, put FileSink, channel, file string) SourceResult {
	absent := SourceResult{Status: StatusAbsent, Reason: channel + ": channel not found"}
	var notes []string
	cov, err := c.Coverage(channel)
	switch {
	case errors.Is(err, ErrChannelNotFound):
		return absent
	case err != nil:
		notes = append(notes, channel+": coverage not read: "+err.Error())
	}
	if cov.AutoBackup {
		notes = append(notes, channel+": auto-backup is on, older events sit in Archive-*.evtx files that were not read")
	}
	f := EventsFile{Log: channel, Coverage: cov}
	if channel == "Application" {
		f.Source = e.EventSource
		if e.EventSource == "" {
			f.NotRun = map[string]string{"2": e.SourceRule, "2c": e.SourceRule, "3b": e.SourceRule}
		}
	}
	var lists [][]Event
	var counter Counter
	bad := 0
	var firstBad error
	for _, q := range BuildEventQueries(e.EventSource, e.Since) {
		if q.Channel != channel {
			continue
		}
		limit := q.Cap
		if limit > 0 && c.capOverride > 0 {
			limit = c.capOverride
		}
		var kept []Event
		// A capped query asks for one event more than its cap: getting it is
		// what tells a truncation from exactly Cap events.
		err := c.Query(ctx, channel, q.XPath, !q.CountOnly, func(x []byte, msg string) bool {
			if ctx.Err() != nil {
				return false
			}
			ev, err := ParseEventXML(x)
			if err != nil {
				if bad++; firstBad == nil {
					firstBad = err
				}
				return true
			}
			if q.CountOnly {
				counter.Add(ev.EventID, ev.TimeUTC)
				return true
			}
			ev.Queries = []string{q.ID}
			ev.Keep(msg)
			kept = append(kept, ev)
			return limit == 0 || len(kept) <= limit
		})
		switch {
		case errors.Is(err, ErrChannelNotFound):
			return absent
		case err != nil:
			return failed(fmt.Errorf("query %s: %w", q.ID, err))
		}
		if limit > 0 && len(kept) > limit {
			kept = kept[:limit]
			if f.Truncated == nil {
				f.Truncated = map[string]string{}
			}
			f.Truncated[q.ID] = kept[limit-1].TimeUTC
		}
		if q.CountOnly {
			f.Counted = counter.Counts()
		}
		// ponytail: queries 1, 2 and 3b of Application are held together
		// until Merge, at most 3 x eventCap events of about 9 KB each once Keep
		// has bounded them (4 KiB of Data, 2,000 characters of message and of
		// Binary, ASCII): about 540 MB live, and the heap runs at about twice
		// the live bytes under the default GOGC, so about 1.1 GB. Measured
		// (harm review, batch D): 105 MiB of peak HeapInuse for 3 x 2,000
		// such events, 317 MiB for 3 x 6,000, so about 1.05 GiB at the caps.
		// Merging as each query ends does not lower that peak, the union
		// being held anyway; a lower eventCap or dataBytes does.
		lists = append(lists, kept)
	}
	if bad > 0 {
		notes = append(notes, fmt.Sprintf("%s: %d events left out, their XML did not parse: %v", channel, bad, firstBad))
	}
	f.Events = Merge(lists...)
	if f.Events == nil {
		f.Events = []Event{}
	}
	if err := writeEvents(put, file, f); err != nil {
		res := failed(err)
		res.Notes = notes
		return res
	}
	return SourceResult{Status: StatusRead, Count: len(f.Events), Notes: notes}
}

// writeEvents writes f as json.MarshalIndent would, one event in memory at a
// time rather than the whole file.
func writeEvents(put FileSink, file string, f EventsFile) error {
	events := f.Events
	f.Events = []Event{}
	head, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	// A JSON string holds no raw newline, so this is the field, not a value.
	at := bytes.Index(head, []byte("\n  \"events\": []"))
	if at < 0 {
		return errors.New("events file: no events field")
	}
	at += len("\n  \"events\": [")
	return streamed(put, file, func(w io.Writer) error {
		if _, err := w.Write(head[:at]); err != nil {
			return err
		}
		for i, ev := range events {
			b, err := json.MarshalIndent(ev, "    ", "  ")
			if err != nil {
				return err
			}
			sep := ",\n    "
			if i == 0 {
				sep = "\n    "
			}
			if _, err := io.WriteString(w, sep); err != nil {
				return err
			}
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		if len(events) > 0 {
			if _, err := io.WriteString(w, "\n  "); err != nil {
				return err
			}
		}
		_, err := w.Write(head[at:])
		return err
	})
}

// streamed gives put what write writes, through a pipe, and returns the first
// error of either; write's goroutine has ended when it returns.
func streamed(put FileSink, file string, write func(io.Writer) error) error {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var err error
		// A panic here is outside readOne's recover, which runs in the
		// caller's goroutine: it would end the process and leave the zip
		// without its central directory.
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("panic: %v", v)
			}
			pw.CloseWithError(err)
			done <- err
		}()
		err = write(pw)
	}()
	err := put(file, pr)
	pr.CloseWithError(errors.New("the sink stopped reading")) // unblocks write if put returned early
	if werr := <-done; err == nil {
		err = werr
	}
	return err
}

func (c Calls) readVSS(ctx context.Context, _ Env, put FileSink) SourceResult {
	out, err := c.PowerShell(ctx, VSSScript)
	if err != nil {
		return failed(err)
	}
	v, err := ParseVSS(out)
	if err != nil {
		return failed(err)
	}
	return written(put, "030.vss.json", v, len(v.Shadows))
}

// readSQLWriter copies SQL Writer's log and its rollovers, the last
// sqlWriterTail bytes of each, newest first within sqlWriterBudget, from the
// directory SqlWriterConfig.ini names; the files past the budget are noted.
// A directory or a file refused is a note, as job outputs record theirs, and
// the source is still read. A file refused as moved under a link replaces the
// notes with one, as job outputs do.
func (c Calls) readSQLWriter(_ context.Context, _ Env, put FileSink) SourceResult {
	shared := c.ProgramFiles + `\Microsoft SQL Server\90\Shared`
	ini, err := c.ReadFile(shared + `\SqlWriterConfig.ini`)
	if errors.Is(err, fs.ErrNotExist) {
		return SourceResult{Status: StatusAbsent, Reason: "no SqlWriterConfig.ini in " + shared}
	}
	if err != nil {
		return failed(err)
	}
	dir, stem, ext := SQLWriterLog(ini, shared)
	final, ok, why := c.DirCheck(dir)
	if !ok {
		return SourceResult{Status: StatusRead, Notes: []string{dir + ": " + why}}
	}
	fsys, err := c.OpenDir(dir)
	var entries []fs.DirEntry
	if err == nil {
		entries, err = fs.ReadDir(fsys, ".")
	}
	if errors.Is(err, fs.ErrNotExist) {
		return SourceResult{Status: StatusAbsent, Reason: dir + " not found"}
	}
	if err != nil {
		return failed(err)
	}
	var logs []fs.FileInfo
	for _, d := range entries {
		if !d.Type().IsRegular() || !IsSQLWriterLog(d.Name(), stem, ext) {
			continue
		}
		info, err := d.Info()
		if err != nil {
			return failed(err)
		}
		logs = append(logs, info)
	}
	sort.SliceStable(logs, func(a, b int) bool { return logs[a].ModTime().After(logs[b].ModTime()) })
	res := SourceResult{Status: StatusRead}
	left := int64(sqlWriterBudget)
	for _, info := range logs {
		src := strings.TrimRight(dir, `\/`) + `\` + info.Name()
		if left < 0 {
			res.Notes = append(res.Notes, src+": budget reached, not copied")
			continue
		}
		f, size, why, err := openIn(c.OpenFile, src, final+`\`+info.Name())
		if err == nil && why == movedUnderLink {
			res.Notes = []string{dir + ": " + errChanged.Error()}
			break
		}
		if err == nil && why != "" {
			res.Notes = append(res.Notes, src+": "+why)
			continue
		}
		var cut bool
		if err == nil {
			// Charged before the copy, as job outputs are: once a file does
			// not fit, every file after it is noted.
			n := min(size, sqlWriterTail)
			if n > left {
				left = -1
				f.Close()
				res.Notes = append(res.Notes, src+": budget reached, not copied")
				continue
			}
			left -= n
			var r io.Reader
			if r, cut, err = TailReader(f, size, sqlWriterTail); err == nil {
				err = put("035.sqlwriter/"+info.Name(), r)
			}
			f.Close()
		}
		if err != nil {
			res.Status, res.Reason = StatusFailed, err.Error()
			break
		}
		res.Copied = append(res.Copied, CopiedFile{Source: src, Archived: "035.sqlwriter/" + info.Name(), Size: size, Cut: cut, tail: sqlWriterTail, Modified: info.ModTime().UTC().Format(time.RFC3339)})
	}
	res.Count = len(res.Copied)
	if res.Status == StatusRead && res.Count == 0 && len(res.Notes) == 0 {
		return SourceResult{Status: StatusAbsent, Reason: "no " + stem + ext + " in " + dir}
	}
	return res
}

func (c Calls) readFilters(_ context.Context, _ Env, put FileSink) SourceResult {
	fl, err := c.Filters()
	if err != nil {
		return failed(err)
	}
	if fl == nil {
		fl = []Filter{}
	}
	return written(put, "040.filter-drivers.json", fl, len(fl))
}

func (c Calls) readPower(_ context.Context, _ Env, put FileSink) SourceResult {
	p, err := c.Power()
	if err != nil {
		return failed(err)
	}
	p.Type = PlanType(p.GUID)
	return written(put, "050.power-plan.json", p, 1)
}

// readExtensions lists the Azure extension logs and copies those of the
// window. No folder means not an Azure VM, or no extension. A folder DirCheck
// refuses is a note over an empty listing, as a refused file is.
func (c Calls) readExtensions(_ context.Context, e Env, put FileSink) SourceResult {
	final, ok, why := c.DirCheck(c.AzurePlugins)
	if !ok {
		res := written(put, "060.vm-extensions.json", []Listed{}, 0)
		res.Notes = []string{c.AzurePlugins + ": " + why}
		return res
	}
	fsys, err := c.OpenDir(c.AzurePlugins)
	var listed []Listed
	if err == nil {
		listed, err = CopyExtensions(fsys, c.AzurePlugins, final, e.Since, extensionBudget, c.OpenFile, put)
	}
	var notes []string
	if errors.Is(err, errChanged) {
		// What was copied was opened at its checked path; the rest of the
		// listing may be another directory's.
		notes, err = []string{c.AzurePlugins + ": " + err.Error()}, nil
		listed = slices.DeleteFunc(listed, func(l Listed) bool { return !l.Copied })
	}
	if errors.Is(err, fs.ErrNotExist) {
		return SourceResult{Status: StatusAbsent, Reason: c.AzurePlugins + " not found: not an Azure VM, or no extension"}
	}
	if err != nil {
		return failed(err)
	}
	var copied []CopiedFile
	for _, l := range listed {
		if l.Copied {
			rel := strings.ReplaceAll(strings.TrimPrefix(l.Path, c.AzurePlugins+`\`), `\`, "/")
			copied = append(copied, CopiedFile{Source: l.Path, Archived: "060.vm-extensions/" + rel, Size: l.Size, Modified: l.Modified})
		}
	}
	if listed == nil {
		listed = []Listed{}
	}
	res := written(put, "060.vm-extensions.json", listed, len(copied))
	res.Copied, res.Notes = copied, notes
	return res
}

// JobOutputStep is one step's entry in 070.job-outputs.json.
type JobOutputStep struct {
	Job        string       `json:"job"`
	StepID     int          `json:"step_id"`
	Step       string       `json:"step"`
	OutputFile string       `json:"output_file"` // the template as typed
	Folder     string       `json:"folder"`      // in the archive
	Readable   bool         `json:"readable"`    // the template expanded to a directory and a pattern
	Notes      []string     `json:"notes,omitempty"`
	Copied     []CopiedFile `json:"copied,omitempty"`
	Error      string       `json:"error,omitempty"`
}

// readJobOutputs copies the output files of every job step. A step that fails
// is recorded with its error and the others are still read.
func (c Calls) readJobOutputs(_ context.Context, e Env, put FileSink) SourceResult {
	switch {
	case e.Facts == nil:
		return SourceResult{Status: StatusSkipped, Reason: "the instance did not answer"}
	case !e.MachineMatch:
		return SourceResult{Status: StatusSkipped, Reason: "the instance is not on this machine"}
	case e.StepsRefused:
		return SourceResult{Status: StatusRefused, Reason: "job steps refused to the login: reading them needs SQLAgentReaderRole in msdb and SELECT on msdb.dbo.sysjobsteps"}
	case e.StepsErr != nil:
		return failed(e.StepsErr)
	}
	logDir := ""
	if MajorVersion(e.Facts.Version) >= 12 { // SQLLOGDIR exists from SQL Server 2014
		logDir = SQLLogDir(e.Facts.ErrorLog)
	}
	names := make([]string, len(e.Steps))
	for i, s := range e.Steps {
		names[i] = fmt.Sprintf("%s_%d", s.Job, s.StepID)
	}
	folders := collect.ResolveDatabaseFolders(names)

	steps := []JobOutputStep{}
	var res SourceResult
	var firstErr error
	left := int64(jobOutputBudget)
	for i, s := range e.Steps {
		p := ExpandOutputFile(s.OutputFile, TokenValues{SQLLogDir: logDir, JobName: s.Job, StepName: s.Step, StepID: s.StepID, JobIDBinary: s.JobIDBinary})
		folder := folders[i].Folder
		copied, notes, err := copyJobOutputs(c, p, folder, e.Since, &left, put)
		st := JobOutputStep{Job: s.Job, StepID: s.StepID, Step: s.Step, OutputFile: s.OutputFile, Folder: "070.job-outputs/" + folder, Readable: p.Readable, Notes: notes, Copied: copied}
		if err != nil {
			st.Error = err.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s step %d: %w", s.Job, s.StepID, err)
			}
		}
		for _, n := range notes {
			res.Notes = append(res.Notes, fmt.Sprintf("%s step %d: %s", s.Job, s.StepID, n))
		}
		res.Copied = append(res.Copied, copied...)
		steps = append(steps, st)
	}
	w := written(put, "070.job-outputs.json", steps, len(res.Copied))
	res.Status, res.Count, res.Reason = w.Status, w.Count, w.Reason
	if firstErr != nil && res.Status == StatusRead {
		res.Status, res.Reason = StatusFailed, firstErr.Error()
	}
	return res
}

func (c Calls) readCluster(ctx context.Context, _ Env, put FileSink) SourceResult {
	out, err := c.PowerShell(ctx, ClusterScript)
	if err != nil {
		return failed(err)
	}
	ci, err := ParseCluster(out)
	if err != nil {
		return failed(err)
	}
	if ci.Absent {
		return SourceResult{Status: StatusAbsent, Reason: `not a cluster node (root\MSCluster: ` + ci.Native + ")"}
	}
	return written(put, "080.cluster.json", ci, len(ci.Nodes))
}
