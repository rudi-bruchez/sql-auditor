package collect

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
)

// The blocked-process-reports writer turns 10.system/063 into one .xml file per
// report plus an index.
//
// It is deliberately shaped like the deadlock writer rather than sharing its
// code. The mechanics rhyme — rank, size, three absences, budget — but the two
// index vocabularies are what a reader reads, and collapsing "deadlocks" and
// "reports" into one generic shape would save a few lines here and cost every
// reader of the archive the word that says what they are looking at.

// maxBlockedProcessReports bounds how many reports are written whole: one per
// episode, its longest, the longest episodes first.
//
// It bounds the FILES, not the rows, and not the episodes. Every report still
// arrives with its timestamp, its size and the fields that place it in an
// episode, and the episodes are built from all of them. The earlier cap kept the
// most recent reports instead; on a client capture of 1 635 it dropped 1 135,
// shrank the window to nineteen hours and tilted every ranking towards the last
// hour. The SAME NUMBER is a literal in 063, and
// TestBlockedProcessCapsAreTheSameNumbersInTheCorpus fails on any drift.
const maxBlockedProcessReports = 500

// maxBlockedProcessBytes bounds one report. A report names two sessions and
// their statements; a megabyte of it means a statement of extraordinary size,
// and the archive rather than the reader is what has to be protected.
const maxBlockedProcessBytes = 1 << 20

type blockedProcessIndex struct {
	Script string `json:"script"`
	// Source is why this directory holds what it holds — which session was
	// read, from which path, and what went wrong if anything did. It is the
	// first thing to look at when the directory is empty, because empty has
	// four different meanings here and only this block tells them apart.
	Source blockedProcessSource `json:"source"`
	Counts blockedProcessCounts `json:"counts"`
	// Episodes is the population, built from every report in the capture and
	// not from the ones written. A report is re-emitted every monitor tick
	// while a block lasts, so counting reports counts each block as many times
	// as its duration divided by the threshold; the episode is the unit a
	// ranking of who blocks whom has to be made in.
	Episodes []blockingEpisode `json:"episodes"`
	Reports  []indexedReport   `json:"reports"`
	// Notes carries the readable form of what Source implies. The analysis
	// layer can derive it; a human opening the archive should not have to.
	Notes     []string        `json:"notes"`
	Omissions []graphOmission `json:"omissions"`
}

type blockedProcessSource struct {
	Session string `json:"session"`
	Path    string `json:"path"`
	// ThresholdSeconds is 0 when 'blocked process threshold (s)' is at its
	// default, and at 0 the event cannot fire at all. Without it, an empty
	// directory reads as "no blocking occurred" when it means "the instance was
	// never asked to look".
	ThresholdSeconds int    `json:"threshold_seconds"`
	ErrorNumber      int    `json:"error_number,omitempty"`
	ErrorMessage     string `json:"error_message,omitempty"`
}

type blockedProcessCounts struct {
	InFiles    int    `json:"in_files"`
	Episodes   int    `json:"episodes"`
	Written    int    `json:"written"`
	Earliest   string `json:"earliest,omitempty"`
	Latest     string `json:"latest,omitempty"`
	CapReports int    `json:"cap_reports"`
	CapBytes   int    `json:"cap_report_bytes"`
}

// blockingEpisode is one block: the same blocked session and transaction, held
// by the same blocking session, on the same resource. The key was measured on a
// capture rather than chosen: ownerId is carried on the blocked side even at
// trancount 0 and absent from the blocking side.
type blockingEpisode struct {
	Episode        int    `json:"episode"`
	BlockedSpid    *int64 `json:"blocked_spid"`
	BlockedOwnerID *int64 `json:"blocked_owner_id"`
	BlockingSpid   *int64 `json:"blocking_spid"`
	WaitResource   string `json:"wait_resource,omitempty"`
	LockMode       string `json:"lock_mode,omitempty"`
	Reports        int    `json:"reports"`
	FirstSeen      string `json:"first_seen"`
	LastSeen       string `json:"last_seen"`
	MaxWaitMs      int64  `json:"max_wait_ms"`
	// The blocker as its longest report saw it. A trancount of 0 separates a
	// statement that holds locks for a long time from a transaction left open,
	// and the two call for different remedies.
	BlockingStatus    string `json:"blocking_status,omitempty"`
	BlockingTrancount *int64 `json:"blocking_trancount"`
	// File is the report kept whole for this episode, empty past the cap.
	File string `json:"file,omitempty"`
}

type indexedReport struct {
	Rank       int64  `json:"rank"`
	OccurredAt string `json:"occurred_at"`
	// Episode is the position of this report's episode in Episodes, 1-based.
	Episode int `json:"episode,omitempty"`
	// FromFile names the .xel the report was read out of. A report from a
	// rollover file and one from the file being written are the same fact, but
	// knowing which is what lets a reader see how far back the capture reaches.
	FromFile string `json:"from_file,omitempty"`
	Bytes    int64  `json:"bytes"`
	File     string `json:"file,omitempty"`
}

// writeBlockedProcessReports is the @writer: blocked-process-reports
// implementation.
func writeBlockedProcessReports(req WriteRequest) (WriteResult, error) {
	root, ok := setByName(req.Sets, "root")
	if !ok {
		return WriteResult{}, fmt.Errorf("blocked-process-reports: result set %q is missing", "root")
	}
	reports, ok := setByName(req.Sets, "reports")
	if !ok {
		return WriteResult{}, fmt.Errorf("blocked-process-reports: result set %q is missing", "reports")
	}

	rel := path.Join(req.Script.Dir, req.Script.Base)
	res := WriteResult{Rel: rel}

	session, _ := stringAt(root, 0, "source.session")
	srcPath, _ := stringAt(root, 0, "source.path")
	errNo, _ := int64At(root, 0, "source.error_number")
	errMsg, _ := stringAt(root, 0, "source.error_message")
	threshold, _ := int64At(root, 0, "blocked_process.threshold_seconds")
	inFiles, _ := int64At(root, 0, "capture.reports_in_files")
	earliest, _ := stringAt(root, 0, "capture.earliest")
	latest, _ := stringAt(root, 0, "capture.latest")

	idx := blockedProcessIndex{
		Script: req.Script.Path,
		Source: blockedProcessSource{
			Session:          session,
			Path:             srcPath,
			ThresholdSeconds: int(threshold),
			ErrorNumber:      int(errNo),
			ErrorMessage:     errMsg,
		},
		Counts: blockedProcessCounts{
			InFiles:    int(inFiles),
			Earliest:   earliest,
			Latest:     latest,
			CapReports: maxBlockedProcessReports,
			CapBytes:   maxBlockedProcessBytes,
		},
		Episodes:  []blockingEpisode{},
		Reports:   []indexedReport{},
		Notes:     []string{},
		Omissions: []graphOmission{},
	}
	episodeOf := groupEpisodes(reports, &idx)

	// FOUR ways this directory ends up empty, and they are different facts about
	// the server. Saying which one applies is the whole job of this block: an
	// archive that shows an empty directory and lets the reader assume "no
	// blocking occurred" would be stating something nobody measured.
	switch {
	case session == "":
		idx.Notes = append(idx.Notes,
			"no Extended Events session on this instance subscribes to blocked_process_report and writes to a file, "+
				"so there is nothing to read. This is not a statement that no blocking occurred: nothing was capturing it. "+
				"10.system/062.xe-sessions.json lists every session and what each captures.")
	case errNo != 0:
		idx.Notes = append(idx.Notes, fmt.Sprintf(
			"reading %s raised SQL error %d: %s. The file is read by the SQL Server service account rather than by "+
				"the login that ran this collection, so a path that exists is not necessarily one it may open.",
			srcPath, errNo, errMsg))
	case inFiles == 0:
		idx.Notes = append(idx.Notes,
			"the capture exists and its files hold no blocked process report. Read this together with the threshold below "+
				"before concluding anything.")
	}
	if threshold == 0 {
		// Appended rather than replacing, because it compounds every case above:
		// a session can exist, be readable, and still never have received an
		// event.
		idx.Notes = append(idx.Notes,
			"'blocked process threshold (s)' is 0, which is the default and means the blocked_process_report event "+
				"never fires. Whatever any session subscribes to, no report can have been produced while this setting "+
				"stands. Setting it is a configuration change and this tool does not make one.")
	}
	// Every note also goes to the manifest. The operator asked for these reports
	// on the command line, so an empty directory is a result they are owed an
	// explanation for at the moment of the run — not one they find later by
	// opening a JSON file inside the archive. check says the same things before
	// a run; this says them after one, from what actually happened.
	for _, n := range idx.Notes {
		req.Warn(fmt.Sprintf("%s: %s", req.Script.Base, n))
	}

	omit := func(rank int64, at, file, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, graphOmission{
			Rank: rank, OccurredAt: at, File: file, Reason: reason, Bytes: size,
		})
		req.Warn(fmt.Sprintf("%s: report %d (%s): %s", req.Script.Base, rank, at, reason))
	}

	fail := func(cause error) (WriteResult, error) {
		n, err := writeBlockedProcessIndex(req, rel, idx)
		res.Bytes += n
		if err != nil {
			req.Warn(fmt.Sprintf("%s: %v", req.Script.Base, err))
		}
		return res, cause
	}

	// Reports past the episode cap are named one by one in the omissions and
	// said ONCE to the manifest. A warning per report put 1 135 identical lines
	// in a client's manifest in September 2026, which is how a real warning
	// next to them goes unread.
	beyondCap := 0
	for r := range reports.Rows {
		rank, _ := int64At(reports, r, "report.rank")
		sample, sampled := int64At(reports, r, "report.sample_rank")
		at, _ := stringAt(reports, r, "occurred_at")
		from, _ := stringAt(reports, r, "file_name")
		size, _ := int64At(reports, r, "report_bytes")
		xml, present := stringAt(reports, r, "report")

		ep := episodeOf[r]
		entry := indexedReport{Rank: rank, OccurredAt: at, Episode: ep, FromFile: from, Bytes: size}
		file := fmt.Sprintf("blocked_process_%04d.xml", rank)

		switch {
		case !sampled:
			// Not its episode's longest report. Nothing is lost: its fields are
			// in the episode, and the episode's longest report is the one kept.
		case sample > maxBlockedProcessReports:
			beyondCap++
			idx.Omissions = append(idx.Omissions, graphOmission{Rank: rank, OccurredAt: at, Bytes: size,
				Reason: fmt.Sprintf("the longest report of episode %d, beyond the %d episodes kept whole; its fields are in the episode", ep, maxBlockedProcessReports)})
		case size > maxBlockedProcessBytes:
			omit(rank, at, "", fmt.Sprintf("the report is %d bytes, above the %d byte cap, so it was not collected", size, maxBlockedProcessBytes), size)
		case !present:
			omit(rank, at, "", "the capture holds no report body for this event", 0)
		default:
			n, ok, err := writeFile(req, rel, file, []byte(xml))
			if err != nil {
				idx.Reports = append(idx.Reports, entry)
				return fail(err)
			}
			if !ok {
				omit(rank, at, file, budgetReason(req.Out.budget), size)
				break
			}
			res.Bytes += n
			res.ReportFiles++
			entry.File = file
			idx.Counts.Written++
			if ep > 0 {
				idx.Episodes[ep-1].File = file
			}
		}
		idx.Reports = append(idx.Reports, entry)
	}
	if beyondCap > 0 {
		req.Warn(fmt.Sprintf("%s: %d episode(s) past the cap of %d were not kept whole; every report's fields are in episodes[] of _index.json",
			req.Script.Base, beyondCap, maxBlockedProcessReports))
	}

	n, err := writeBlockedProcessIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// groupEpisodes builds idx.Episodes from every row and returns, per row, the
// 1-based position of its episode. Episodes are ordered longest wait first, so
// the numbering agrees with the order in which 063 keeps reports whole.
func groupEpisodes(reports ResultSet, idx *blockedProcessIndex) []int {
	nullable := func(r int, col string) *int64 {
		if v, ok := int64At(reports, r, col); ok {
			return &v
		}
		return nil
	}
	key := func(r int) string {
		part := func(p *int64) string {
			if p == nil {
				return "-"
			}
			return fmt.Sprint(*p)
		}
		res, _ := stringAt(reports, r, "blocked.wait_resource")
		return part(nullable(r, "blocked.spid")) + "|" + part(nullable(r, "blocked.owner_id")) +
			"|" + part(nullable(r, "blocking.spid")) + "|" + res
	}

	byKey := map[string]*blockingEpisode{}
	var order []*blockingEpisode
	rowKey := make([]string, len(reports.Rows))
	for r := range reports.Rows {
		k := key(r)
		rowKey[r] = k
		at, _ := stringAt(reports, r, "occurred_at")
		wait, _ := int64At(reports, r, "blocked.wait_ms")
		e, seen := byKey[k]
		if !seen {
			res, _ := stringAt(reports, r, "blocked.wait_resource")
			e = &blockingEpisode{BlockedSpid: nullable(r, "blocked.spid"),
				BlockedOwnerID: nullable(r, "blocked.owner_id"), BlockingSpid: nullable(r, "blocking.spid"),
				WaitResource: res, FirstSeen: at, LastSeen: at, MaxWaitMs: -1}
			byKey[k] = e
			order = append(order, e)
		}
		e.Reports++
		if at < e.FirstSeen {
			e.FirstSeen = at
		}
		if at > e.LastSeen {
			e.LastSeen = at
		}
		if wait > e.MaxWaitMs {
			e.MaxWaitMs = wait
			e.LockMode, _ = stringAt(reports, r, "blocked.lock_mode")
			e.BlockingStatus, _ = stringAt(reports, r, "blocking.status")
			e.BlockingTrancount = nullable(r, "blocking.trancount")
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].MaxWaitMs > order[j].MaxWaitMs })
	position := map[string]int{}
	for i, e := range order {
		e.Episode = i + 1
		if e.MaxWaitMs < 0 {
			e.MaxWaitMs = 0
		}
		idx.Episodes = append(idx.Episodes, *e)
	}
	for k, e := range byKey {
		position[k] = e.Episode
	}
	idx.Counts.Episodes = len(order)
	out := make([]int, len(reports.Rows))
	for r, k := range rowKey {
		out[r] = position[k]
	}
	return out
}

func writeBlockedProcessIndex(req WriteRequest, rel string, idx blockedProcessIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("blocked-process-reports: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.writeUnbudgeted(path.Join(rel, "_index.json"), b)
}
