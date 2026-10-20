package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// ErrNotWindows is what every other system answers.
var ErrNotWindows = errors.New("sql-auditor host reads the Windows server it runs on")

// Status is what became of one source.
type Status string

const (
	StatusRead    Status = "read"
	StatusAbsent  Status = "absent"  // legitimately not there: no cluster, no Azure agent
	StatusRefused Status = "refused" // by a permission
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped" // the instance did not answer, or not on this machine
)

// SourceResult is one source's line of the manifest and of stdout.
type SourceResult struct {
	Name   string
	Status Status
	Count  int
	Reason string
	Copied []CopiedFile // listed in the manifest with source path and size
	Notes  []string     // the manifest's warnings
}

// Env is what Run learned before reading the sources.
type Env struct {
	Facts        *InstanceFacts // nil when the instance did not answer
	MachineMatch bool
	LocalNetBIOS string
	EventSource  string // "" when undecided
	SourceRule   string
	Since        time.Time
	Steps        []JobStep
	StepsRefused bool
	StepsErr     error // the steps could not be read, for a reason other than a refusal
}

// Source is one archive entry family, read in turn by Run.
type Source struct {
	Name string
	Read func(ctx context.Context, e Env, put FileSink) SourceResult
}

// Platform is what Run asks the machine and the instance.
type Platform struct {
	Check    func() error // Windows and elevated
	NetBIOS  func() (string, error)
	Services func() ([]string, error)
	Facts    func(ctx context.Context) (*InstanceFacts, error)
	Steps    func(ctx context.Context) ([]JobStep, bool, error)
	Sources  []Source
}

// Options are the command's.
type Options struct {
	To, EnvServer, Version string
	Config                 *collect.Config
	Since                  time.Time
	Now                    func() time.Time
	Stdout, Stderr         io.Writer
	Platform               Platform

	wrapArchive func(io.Writer) io.Writer // tests only: inject a writer that fails
}

// MachineMatches is the machine check of docs/memory-dumps-spec.md: the
// instance's ComputerNamePhysicalNetBIOS against this machine's, ignoring case.
// An empty name matches nothing.
func MachineMatches(serverNetBIOS, localNetBIOS string) bool {
	s, l := strings.TrimSpace(serverNetBIOS), strings.TrimSpace(localNetBIOS)
	return s != "" && strings.EqualFold(s, l)
}

// ArchiveName is <instance>-<yyyy-mm-dd>-<hhmmss>.host, the zip's name without
// .zip and its top-level directory.
func ArchiveName(instance string, t time.Time) string {
	return collect.SafeFolderName(instance) + "-" + t.Format("2006-01-02-150405") + ".host"
}

// run is what the manifest's header reports.
type run struct {
	o          Options
	env        Env
	now        time.Time
	factsErr   error
	netbiosErr error
	results    []SourceResult
}

// Run reads every source into one archive in o.To and returns the exit code:
// 0 when every source was read, absent or refused, 2 otherwise, when the job
// steps could not be read, or when no archive was written.
func Run(ctx context.Context, o Options) int {
	p := o.Platform
	if err := p.Check(); err != nil {
		fmt.Fprintln(o.Stderr, err)
		return 2
	}
	// Stat only: the spec allows no file in --to but the archive, so a
	// directory that cannot be written is found by creating the archive.
	if fi, err := os.Stat(o.To); err != nil || !fi.IsDir() {
		fmt.Fprintf(o.Stderr, "--to %s is not an existing directory\n", o.To)
		return 2
	}
	r := &run{o: o, now: o.Now()}
	r.env.Since = o.Since
	if p.Facts != nil {
		r.env.Facts, r.factsErr = p.Facts(ctx)
	} else {
		r.factsErr = errors.New("no connection")
	}
	if p.NetBIOS != nil {
		r.env.LocalNetBIOS, r.netbiosErr = p.NetBIOS()
	} else {
		r.netbiosErr = errors.New("not available")
	}
	if r.env.Facts != nil {
		r.env.MachineMatch = MachineMatches(r.env.Facts.NetBIOS, r.env.LocalNetBIOS)
	}
	// Without the list of services, only the instance's own name can decide.
	var services []string
	if p.Services != nil {
		services, _ = p.Services()
	}
	in := SourceInput{Answered: r.env.Facts != nil, MachineMatch: r.env.MachineMatch, EnvServer: o.EnvServer, LocalServices: services}
	if r.env.Facts != nil {
		in.InstanceName = r.env.Facts.InstanceName
	}
	r.env.EventSource, r.env.SourceRule = ChooseEventSource(in)
	if r.env.Facts != nil && r.env.MachineMatch && p.Steps != nil {
		var err error
		r.env.Steps, r.env.StepsRefused, err = p.Steps(ctx)
		if err != nil && !r.env.StepsRefused {
			r.env.StepsErr = err
		}
	}

	instance := r.env.LocalNetBIOS
	if r.env.Facts != nil {
		instance = collect.RunServerName(r.env.Facts.ServerName, o.Config)
	}
	if strings.TrimSpace(instance) == "" {
		instance = "unknown"
	}
	a, err := createArchive(o.To, ArchiveName(instance, r.now), o.wrapArchive)
	if err != nil {
		fmt.Fprintln(o.Stderr, err)
		return 2
	}
	abort := func(why string) int {
		if err := a.Remove(); err != nil {
			fmt.Fprintf(o.Stderr, "%s; %s could not be removed: %v\n", why, a.Path(), err)
		} else {
			fmt.Fprintf(o.Stderr, "%s; %s removed\n", why, a.Path())
		}
		return 2
	}
	for _, s := range p.Sources {
		if ctx.Err() != nil {
			break
		}
		res := readOne(ctx, s, r.env, a.Put)
		r.results = append(r.results, res)
		fmt.Fprintf(o.Stdout, "%s: %s (%d)\n", res.Name, res.Status, res.Count)
		if a.Err() != nil {
			return abort("writing the archive: " + a.Err().Error())
		}
	}
	if ctx.Err() != nil {
		return abort("interrupted")
	}
	a.Put("MANIFEST.txt", strings.NewReader(r.manifest()))
	if err := a.Close(); err != nil {
		return abort("writing the archive: " + err.Error())
	}

	code := 0
	// A source may ignore StepsErr; the run does not.
	if r.env.Facts == nil || !r.env.MachineMatch || r.env.StepsErr != nil {
		code = 2
	}
	for _, res := range r.results {
		if res.Status == StatusFailed {
			code = 2
		}
	}
	fmt.Fprintln(o.Stdout, a.Path())
	return code
}

// readOne reads one source, a panic becoming the source failed: one broken
// source does not cost the others.
func readOne(ctx context.Context, s Source, e Env, put FileSink) (res SourceResult) {
	defer func() {
		if v := recover(); v != nil {
			res = SourceResult{Status: StatusFailed, Reason: fmt.Sprint("panic: ", v)}
		}
		res.Name = s.Name
	}()
	return s.Read(ctx, e, put)
}
