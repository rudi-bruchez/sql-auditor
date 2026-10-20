package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	// message file is gone. each returns false to stop.
	Query      func(ctx context.Context, channel, xpath string, each func(eventXML []byte, message string) bool) error
	Coverage   func(channel string) (Coverage, error)
	PowerShell func(ctx context.Context, script string) ([]byte, error)
	Filters    func() ([]Filter, error)
	Power      func() (PowerPlan, error)
	OpenDir    func(dir string) (fs.FS, error)
	ReadFile   func(path string) ([]byte, error)
	PathCheck  PathCheck

	ProgramFiles, AzurePlugins string

	capOverride int // tests only: replaces every non-zero Cap
}

// extensionBudget is the most of the VM extension logs copied.
const extensionBudget = 50 << 20

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
		err := c.Query(ctx, channel, q.XPath, func(x []byte, msg string) bool {
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
			ev.Message = CutMessage(msg)
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
		lists = append(lists, kept)
	}
	if bad > 0 {
		notes = append(notes, fmt.Sprintf("%s: %d events left out, their XML did not parse: %v", channel, bad, firstBad))
	}
	f.Events = Merge(lists...)
	if f.Events == nil {
		f.Events = []Event{}
	}
	res := written(put, file, f, len(f.Events))
	res.Notes = notes
	return res
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

// readSQLWriter copies SQL Writer's log and its rollovers whole, from the
// directory SqlWriterConfig.ini names.
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
	res := SourceResult{Status: StatusRead}
	for _, d := range entries {
		if !d.Type().IsRegular() || !IsSQLWriterLog(d.Name(), stem, ext) {
			continue
		}
		src := strings.TrimRight(dir, `\/`) + `\` + d.Name()
		if ok, why := c.PathCheck(src); !ok {
			res.Notes = append(res.Notes, src+": "+why)
			continue
		}
		info, err := d.Info()
		if err == nil {
			err = copyFile(fsys, d.Name(), info.Size(), "035.sqlwriter/"+d.Name(), put)
		}
		if err != nil {
			res.Status, res.Reason = StatusFailed, err.Error()
			break
		}
		res.Copied = append(res.Copied, CopiedFile{Source: src, Archived: "035.sqlwriter/" + d.Name(), Size: info.Size(), Modified: info.ModTime().UTC().Format(time.RFC3339)})
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
// window. No folder means not an Azure VM, or no extension.
func (c Calls) readExtensions(_ context.Context, e Env, put FileSink) SourceResult {
	fsys, err := c.OpenDir(c.AzurePlugins)
	var listed []Listed
	if err == nil {
		listed, err = CopyExtensions(fsys, c.AzurePlugins, e.Since, extensionBudget, c.PathCheck, put)
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
	res.Copied = copied
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
		return SourceResult{Status: StatusRefused, Reason: "msdb.dbo.sysjobsteps refused to the login"}
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
	for i, s := range e.Steps {
		p := ExpandOutputFile(s.OutputFile, TokenValues{SQLLogDir: logDir, JobName: s.Job, StepName: s.Step, StepID: s.StepID, JobIDBinary: s.JobIDBinary})
		folder := folders[i].Folder
		copied, notes, err := CopyJobOutputs(c.OpenDir, p, folder, e.Since, c.PathCheck, put)
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
