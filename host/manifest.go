package host

import (
	"fmt"
	"strings"
	"time"
)

// disclosure is the spec's "What it writes" paragraph on what the client
// should review before the archive leaves the building.
const disclosure = `Before you send this archive

Read it first: it may hold what your organization does not send outside.
Computer, cluster and volume names, IP addresses, file paths and the text of
event messages are in it. The System and Application logs, the cluster channel
and SQL Writer's log cover every instance on this host, so an archive taken on a
host with other instances names their errors and their databases too.

Above all, review the copied text files. A job output file is whatever the step
printed, and a CmdExec or PowerShell step can print a connection string or a
password. The Azure extension logs and SQL Writer's log name databases and
accounts. When SQL Writer is configured with its own TraceFile, every file of
that directory named after it and followed by digits is copied.
`

// manifest is MANIFEST.txt.
func (r *run) manifest() string {
	var b strings.Builder
	e := r.env
	fmt.Fprintf(&b, "sql-auditor host %s\n\n", r.o.Version)
	fmt.Fprintf(&b, "Host:          %s%s\n", e.LocalNetBIOS, errSuffix(r.netbiosErr))
	if e.Facts != nil {
		fmt.Fprintf(&b, "Instance:      %s, version %s\n", e.Facts.ServerName, e.Facts.Version)
	} else {
		fmt.Fprintf(&b, "Instance:      did not answer%s\n", errSuffix(r.factsErr))
	}
	fmt.Fprintf(&b, "Run (UTC):     %s\n", r.now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Window:        from %s (UTC %s)\n", e.Since.Format("2006-01-02 15:04 MST"), e.Since.UTC().Format(time.RFC3339))
	b.WriteString("Elevation:     elevated, checked before any read\n")
	switch {
	case e.Facts == nil:
		fmt.Fprintf(&b, "Machine check: not run, the instance did not answer; this machine is %s\n", e.LocalNetBIOS)
	case e.MachineMatch:
		fmt.Fprintf(&b, "Machine check: match, instance on %s, this machine %s\n", e.Facts.NetBIOS, e.LocalNetBIOS)
	default:
		fmt.Fprintf(&b, "Machine check: MISMATCH, instance on %s, this machine %s; job output files skipped\n", e.Facts.NetBIOS, e.LocalNetBIOS)
	}
	source := e.EventSource
	if source == "" {
		source = "undecided, the instance's own event queries are not run"
	}
	fmt.Fprintf(&b, "Event source:  %s (rule: %s)\n", source, e.SourceRule)
	if e.StepsErr != nil {
		fmt.Fprintf(&b, "Job steps:     could not be read: %v\n", e.StepsErr)
	}

	b.WriteString("\nSources\n\n")
	for _, s := range r.results {
		fmt.Fprintf(&b, "%s: %s (%d)", s.Name, s.Status, s.Count)
		if s.Reason != "" {
			fmt.Fprintf(&b, ": %s", s.Reason)
		}
		b.WriteString("\n")
	}

	b.WriteString("\nNotes\n\n")
	for _, s := range r.results {
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "%s: %s\n", s.Name, n)
		}
	}

	b.WriteString("\nCopied files\n\n")
	for _, s := range r.results {
		for _, c := range s.Copied {
			cut := ""
			if c.Cut {
				cut = ", last 1 MiB only"
			}
			fmt.Fprintf(&b, "%s -> %s (%d bytes%s)\n", c.Source, c.Archived, c.Size, cut)
		}
	}

	b.WriteString("\n" + disclosure)
	return b.String()
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}
