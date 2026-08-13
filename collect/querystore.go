package collect

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// The Query Store extraction is the one collector that produces a directory
// instead of a document: one file per query, one per plan, and an index. The
// pipeline's own writer cannot express that, so @writer routes these two
// scripts here.
//
// Everything upstream is unchanged. The scripts are discovered, linted,
// version-gated, flag-gated, run per database and timed by the same code as
// every other collector; only the last step differs.

// maxPlanBytes bounds what a single execution plan may write. maxRunBytes,
// declared beside the run writer, bounds the whole run. They are constants
// rather than options because an operator has no way to pick a good value
// before seeing the plans, and an omission is recorded rather than silently
// applied: a truncation nobody is told about reads as "everything is here".
//
// The SAME NUMBER is written out as a literal in 021 and 022, in their
// DATALENGTH guard, so a DBA reading the exported file sees the cap. That
// duplication is load-bearing and is not left to a comment to enforce:
// TestPlanCapIsTheSameNumberInTheCorpus reads both files and fails on any
// drift. It has to, because a Go constant raised above a stale SQL literal
// makes an oversized plan arrive as a NULL with no size beside it, and the
// writer would then state "the Query Store holds no plan XML for this
// plan_id" — a false fact about the server, which is the exact failure the
// omissions exist to prevent.
const maxPlanBytes = 8 << 20

// WriteRequest is everything a writer is given. It is deliberately not a
// directory path.
type WriteRequest struct {
	// Out is the only way a writer reaches the filesystem. Every byte has to
	// pass the Showplan inspection and the run budget, and a writer holding a
	// path could bypass both.
	Out    *runWriter
	Script Script
	Unit   DatabaseFolder
	Sets   []NamedResultSet
	State  *QueryStoreState
	// Warn puts a line in the manifest's warnings. Every omission goes through
	// it as well as into _index.json, so a reader who only opens MANIFEST.txt
	// still learns that something was left out.
	Warn func(string)
}

// WriteResult is what the manifest needs from a writer: the run-folder-relative
// path it records, the bytes it accounts for, and how many plan and text files
// were actually written.
//
// Both counts drive the disclosure, and both count files written rather than
// rows considered. Plans alone would not do: a natively compiled procedure has
// a NULL query_plan and a run that exhausts its budget stops after the texts,
// and either case still leaves the verbatim SQL of a production workload in the
// archive. An archive whose manifest says it holds none would be the very
// defect the disclosure section exists to prevent, reached from the other side.
//
// The counts and Bytes are valid even when the writer returns a non-nil error.
// A writer that fails halfway has already put files on disk, and a caller that
// discarded the counts on error would compute the disclosure from a subset of
// what the archive actually holds. Accumulate them either way.
type WriteResult struct {
	Rel       string
	Bytes     int
	PlanFiles int
	TextFiles int
}

// ScriptWriter turns materialised result sets into files.
type ScriptWriter func(WriteRequest) (WriteResult, error)

// writerFor resolves an @writer name to its implementation. It returns nil both
// for a name it does not know and for a name that is declared in KnownWriters
// but not yet implemented, so the caller can refuse the script rather than fall
// back to the encoder and quietly emit a document where a directory was meant.
//
// The two sets are meant to stay in step, and TestWriterForCoversKnownWriters
// fails the moment they drift, listing the one gap that is expected today.
func writerFor(name string) ScriptWriter {
	switch name {
	case "query-store-detail":
		return writeQueryStoreDetail
	case "query-store-profiled":
		return writeQueryStoreProfiled
	}
	return nil
}

// QueryStoreState carries the selection from 021 to 022. 022 needs to know
// which queries were retained, and re-deriving the ranking would both cost a
// second pass and risk drifting: the Query Store keeps recording between the
// two scripts, so the second ranking need not match the first.
type QueryStoreState struct {
	Selected map[string][]int64
	// From and To are the window, already resolved from server local time to
	// instants using the offset the probe reported. Resolving them here rather
	// than in Resolve is not tidiness: Resolve runs before anything has spoken
	// to the server, and a local timestamp resolved with the collecting
	// machine's zone is wrong in a way nothing downstream can detect.
	From, To time.Time
}

func NewQueryStoreState() *QueryStoreState {
	return &QueryStoreState{Selected: map[string][]int64{}}
}

// omission is one thing the archive does not contain and the reason why. It is
// written even when the reason is mundane, because the alternative — a plan
// file that is simply absent — is indistinguishable from a collector that never
// looked.
type omission struct {
	QueryID int64 `json:"query_id"`
	PlanID  int64 `json:"plan_id"`
	// File names the file this omission is about, when one file is at stake:
	// a budget refusal on query_11.sql and one on query_11.stats.json are
	// different losses, and the query and plan ids alone do not tell them
	// apart. Empty when no single file is — a query for which nothing at all
	// was found names none.
	File   string `json:"file,omitempty"`
	Reason string `json:"reason"`
	// Bytes is the plan's size when the server reported one, and 0 when nobody
	// knows it — a plan the Query Store does not hold has no size.
	Bytes int64 `json:"bytes,omitempty"`
}

type indexedQuery struct {
	QueryID int64 `json:"query_id"`
	// PlanIDs, not Plans: it sits beside plan_files and carries plan ids,
	// while the profiled index's plans carries objects. One archive must not
	// use one name for two shapes.
	PlanIDs []int64 `json:"plan_ids"`
	// Ranks holds this query's position in each of the four rankings, keyed by
	// duration, cpu, logical_reads, executions. All four are always present and
	// none is capped: a query can be retained on one metric while sitting far
	// down another, and that contrast is the finding, not noise to suppress.
	Ranks     map[string]int64 `json:"ranks"`
	Forced    bool             `json:"forced"`
	TextFile  string           `json:"text_file"`
	StatsFile string           `json:"stats_file"`
	PlanFiles []string         `json:"plan_files"`
}

// selectionCounts reports the cap and what was retained as three separate
// numbers. A reader seeing cap 50, ranked 50 and forced 3 must not conclude the
// cap was exceeded: a forced plan is kept because it is forced, and was never
// subject to the ranking the cap applies to.
//
// Ranked counts the queries that entered on the ranking — those the SQL's
// LEFT JOIN to the rankings gave a rank at all — and never the whole index. A
// query selected only because one of its plans is forced arrives with all four
// ranks NULL and belongs in Forced alone; counting it here would report a
// ranked population larger than the cap, which reads as a leak.
//
// The two populations OVERLAP, and the overlap is not an error: a query that
// the ranking retained AND that has a forced plan is counted in both, because
// it is a member of both. Ranked + Forced is therefore not the number of
// queries in the index — len(queries) is, and it is right there beside these
// three.
type selectionCounts struct {
	Cap    int64 `json:"cap"`
	Ranked int   `json:"ranked"`
	Forced int   `json:"forced"`
}

// detailIndex is the directory's table of contents. It exists even when nothing
// else does, because "the Query Store is off here" and "the collector never ran
// here" are different facts and an empty directory states neither.
type detailIndex struct {
	Database  string          `json:"database"`
	Script    string          `json:"script"`
	State     json.RawMessage `json:"state"`
	Window    json.RawMessage `json:"window"`
	Selection selectionCounts `json:"selection"`
	Queries   []indexedQuery  `json:"queries"`
	Omissions []omission      `json:"omissions"`
}

// rankMetrics is fixed and ordered so the index is byte-stable between runs.
var rankMetrics = []string{"duration", "cpu", "logical_reads", "executions"}

// queryFileStem is the one place the naming convention lives. The files are
// copied out of this directory into a plan analyser, often several at once, and
// a name reduced to "11.101.sqlplan" says nothing once it has left its folder.
func queryFileStem(queryID int64) string { return fmt.Sprintf("query_%d", queryID) }

func planFileName(queryID, planID int64, suffix string) string {
	return fmt.Sprintf("%s.plan_%d%s.sqlplan", queryFileStem(queryID), planID, suffix)
}

// writeQueryStoreDetail turns the three result sets of 021 into one directory
// for the database it ran against.
func writeQueryStoreDetail(req WriteRequest) (WriteResult, error) {
	root, ok := setByName(req.Sets, "root")
	if !ok {
		return WriteResult{}, fmt.Errorf("query-store-detail: result set %q is missing", "root")
	}
	selected, ok := setByName(req.Sets, "selected")
	if !ok {
		return WriteResult{}, fmt.Errorf("query-store-detail: result set %q is missing", "selected")
	}
	intervals, ok := setByName(req.Sets, "intervals")
	if !ok {
		return WriteResult{}, fmt.Errorf("query-store-detail: result set %q is missing", "intervals")
	}

	rel := path.Join(req.Script.Dir, req.Unit.Folder, req.Script.Base)
	res := WriteResult{Rel: rel}

	state, _ := stringAt(root, 0, "state.actual")
	cap64, _ := int64At(root, 0, "selection.cap")
	stateRaw, windowRaw, err := indexHeader(root, func(msg string) {
		req.Warn(fmt.Sprintf("%s: _index.json: %s", req.Unit.Name, msg))
	})
	if err != nil {
		return WriteResult{}, err
	}
	idx := detailIndex{
		Database:  req.Unit.Name,
		Script:    req.Script.Path,
		State:     stateRaw,
		Window:    windowRaw,
		Selection: selectionCounts{Cap: cap64},
		Queries:   []indexedQuery{},
		Omissions: []omission{},
	}

	// From here on this writer has run against this database and will leave an
	// _index.json behind, so the selection is published even when it is empty.
	// The manifest reads a MISSING entry as "021 delivered nothing at all" and
	// warns about it; an empty entry as "021 ran here and retained nothing,
	// which its own index already records". Leaving the OFF path or a mid-way
	// error without an entry accuses a collector that did exactly its job. The
	// real selection replaces this at the end.
	req.State.Selected[req.Unit.Name] = []int64{}

	// A database whose Query Store is switched off is a skip, not a failure and
	// not an empty success. The index still has to be written, or the analysis
	// layer cannot tell the two apart.
	//
	// A MISSING or empty state is read as OFF, which is what 021 says it will
	// be: its root row comes from a LEFT JOIN from sys.databases, so a database
	// on which the Query Store has never been enabled returns no row in
	// sys.database_query_store_options and state.actual arrives NULL. Testing
	// "OFF" alone would take the full path on exactly those databases and write
	// an index whose state is null.
	if state == "" || state == "OFF" || len(root.Rows) == 0 {
		n, err := writeIndex(req, rel, idx)
		res.Bytes += n
		return res, err
	}

	omit := func(queryID, planID int64, file, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, omission{QueryID: queryID, PlanID: planID, File: file, Reason: reason, Bytes: size})
		req.Warn(fmt.Sprintf("%s: query %d, plan %d: %s", req.Unit.Name, queryID, planID, reason))
	}

	// fail writes the index before letting a write error leave this writer. By
	// the time one occurs — a full disk, a share withdrawn mid-run, permissions
	// dropped under a running collection — query texts and plans are already on
	// disk, and returning without an index leaves that directory undescribed:
	// the very thing _index.json exists to prevent, and the reason it goes
	// through writeUnbudgeted at all. The budget path has always got this
	// right; only the error path leaked. The error still propagates.
	//
	// A failure of the index write itself is DISCARDED in favour of the
	// original. The first error is the cause and the second its consequence —
	// the disk that just refused a plan is the same disk refusing the index —
	// and a caller told only "could not write _index.json" would look for the
	// fault in the wrong place. It goes to the manifest as a warning rather
	// than being lost.
	fail := func(cause error) (WriteResult, error) {
		n, err := writeIndex(req, rel, idx)
		res.Bytes += n
		if err != nil {
			req.Warn(fmt.Sprintf("%s: %v", req.Unit.Name, err))
		}
		return res, cause
	}

	// abandon puts the query being written into the index as far as it got, and
	// then fails. It claims nothing that is not on disk: the statistics file is
	// written last, so it is never there when this is reached, and the caller
	// clears the text file when it was that write which failed. Counting it in
	// Ranked here keeps the count and the list of queries describing the same
	// set.
	abandon := func(entry indexedQuery, cause error) (WriteResult, error) {
		entry.StatsFile = ""
		if len(entry.Ranks) > 0 {
			idx.Selection.Ranked++
		}
		idx.Queries = append(idx.Queries, entry)
		return fail(cause)
	}

	// First-seen order, not sorted: the SQL already emits the round robin's
	// order, and re-sorting here would hide which metric brought a query in.
	var ids []int64
	seen := map[int64]bool{}
	for r := range selected.Rows {
		id, ok := int64At(selected, r, "query_id")
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}

	for _, id := range ids {
		rows := rowsWhere(selected, "query_id", id)
		entry := indexedQuery{
			QueryID:   id,
			PlanIDs:   []int64{},
			Ranks:     map[string]int64{},
			TextFile:  queryFileStem(id) + ".sql",
			StatsFile: queryFileStem(id) + ".stats.json",
			PlanFiles: []string{},
		}
		// Forced is a property of the SELECTION, not of a plan row, and it is
		// read from the column the SQL projects for exactly this. Deriving it
		// from is_forced — that is, from p.is_forced_plan — misses the case the
		// forced CTE exists to catch: a plan with force_failure_count > 0 and
		// is_forced_plan = 0 is a forcing decision that has silently stopped
		// being applied, which is the whole reason it is collected, and it would
		// have been recorded here as not forced at all. Counting per query
		// rather than per plan matters for the same reason the cap does: one
		// query with two forced plans is one query.
		if boolAt(rows, 0, "is_forced_selection") {
			entry.Forced = true
			idx.Selection.Forced++
		}
		for _, metric := range rankMetrics {
			if v, ok := int64At(rows, 0, "rank."+metric); ok {
				// int64At leaves a NULL out of the map rather than storing zero,
				// which would read as "first".
				entry.Ranks[metric] = v
			}
		}

		// The text: the first non-NULL one. Every row of a query carries the
		// same text, one row per plan.
		text := ""
		haveText := false
		for r := range rows.Rows {
			if s, ok := stringAt(rows, r, "text"); ok {
				text, haveText = s, true
				break
			}
		}
		if !haveText {
			// The file is still written, empty: over-disclosure is the safe
			// direction and a missing file would be read as a collector that
			// never looked. But an empty .sql and a query whose text the Query
			// Store no longer holds are different facts, and without this line
			// the index would claim a text file that carries no text.
			omit(id, 0, entry.TextFile, "the Query Store holds no text for this query_id; the file was written empty", 0)
		}
		if n, ok, err := writeFile(req, rel, entry.TextFile, []byte(text)); err != nil {
			// The text is the first file written for a query, and this one did
			// not land: the entry names neither it nor anything after it, and
			// the queries completed before it stay described.
			entry.TextFile = ""
			return abandon(entry, err)
		} else if ok {
			res.Bytes += n
			res.TextFiles++
		} else {
			omit(id, 0, entry.TextFile, budgetReason(req.Out.budget), 0)
			entry.TextFile = ""
		}

		// Deduplicated like the query ids above. A repeated (query_id, plan_id)
		// would write the same filename twice, count the plan and its bytes
		// twice, and list the name twice in the index — three lies about the
		// same file.
		seenPlan := map[int64]bool{}
		for r := range rows.Rows {
			planID, _ := int64At(rows, r, "plan_id")
			if seenPlan[planID] {
				continue
			}
			seenPlan[planID] = true
			entry.PlanIDs = append(entry.PlanIDs, planID)

			size, _ := int64At(rows, r, "query_plan_bytes")
			plan, present := stringAt(rows, r, "query_plan")
			name := planFileName(id, planID, "")
			switch {
			case !present && size > maxPlanBytes:
				// The SQL's DATALENGTH guard nulled it out before it crossed the
				// wire. Reported apart from a plan that never existed, because
				// conflating them turns a plan we chose not to fetch into one the
				// server does not have.
				omit(id, planID, name, fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap and was not sent", size, maxPlanBytes), size)
			case !present:
				omit(id, planID, name, "the Query Store holds no plan XML for this plan_id", 0)
			case len(plan) > maxPlanBytes:
				// The backstop, for when the SQL guard has been edited away.
				omit(id, planID, name, fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap", len(plan), maxPlanBytes), int64(len(plan)))
			default:
				n, ok, err := writeFile(req, rel, name, []byte(plan))
				if err != nil {
					// The plans written before this one are named; this one is
					// not, and neither is the statistics file that never got its
					// turn.
					return abandon(entry, err)
				}
				if !ok {
					omit(id, planID, name, budgetReason(req.Out.budget), int64(len(plan)))
					continue
				}
				res.Bytes += n
				res.PlanFiles++
				entry.PlanFiles = append(entry.PlanFiles, name)
			}
		}

		// Both sets go in as arrays: RootSetName merges into the document's top
		// level and must be a single row, which neither of these is. The text
		// and the plan XML are dropped because they already have their own
		// files, and repeating the XML here would double the archive.
		stats, warns, err := Encode([]NamedResultSet{
			{Spec: ResultSpec{Name: "plans", Shape: ShapeArray}, Set: withoutColumns(rows, "text", "query_plan")},
			{Spec: ResultSpec{Name: "intervals", Shape: ShapeArray}, Set: rowsWhere(intervals, "query_id", id)},
		})
		if err != nil {
			return abandon(entry, fmt.Errorf("query-store-detail: encoding statistics for query %d: %w", id, err))
		}
		for _, w := range warns {
			req.Warn(fmt.Sprintf("%s: query %d: %s", req.Unit.Name, id, w))
		}
		if n, ok, err := writeFile(req, rel, entry.StatsFile, stats); err != nil {
			return abandon(entry, err)
		} else if ok {
			res.Bytes += n
		} else {
			omit(id, 0, entry.StatsFile, budgetReason(req.Out.budget), int64(len(stats)))
			entry.StatsFile = ""
		}

		// Ranked is counted here, from the ranks the query actually has. The SQL
		// LEFT JOINs the rankings, so a query that entered only because a plan
		// of its own is forced arrives with all four NULL and int64At leaves
		// them out of the map: a non-empty map IS "entered on the ranking".
		if len(entry.Ranks) > 0 {
			idx.Selection.Ranked++
		}
		idx.Queries = append(idx.Queries, entry)
	}

	req.State.Selected[req.Unit.Name] = ids

	n, err := writeIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// budgetReason is the one sentence for a refusal by the run budget. It names
// the cap rather than saying "too large", because the file itself may be small
// and the reader would otherwise look for the fault in the wrong place.
//
// It DERIVES the number from the budget instead of writing it out: the budget
// is a runWriter field, and a sentence carrying 256 MiB states a size the
// archive did not actually run under the moment that field says otherwise.
// The cap is not the Query Store's either — it bounds every collector in the
// run together — so it is no longer called an extraction cap.
func budgetReason(budget int) string {
	const mib = 1 << 20
	if budget >= mib && budget%mib == 0 {
		return fmt.Sprintf("the run reached the %d MiB cap on what the whole collection may write", budget/mib)
	}
	return fmt.Sprintf("the run reached the %d byte cap on what the whole collection may write", budget)
}

// writeFile puts one payload in the directory. It reports false, without an
// error, when the run budget cannot take it: an exhausted budget is a recorded
// omission, not a failed collection.
func writeFile(req WriteRequest, rel, name string, payload []byte) (int, bool, error) {
	if req.Out.overBudget() || req.Out.spent+len(payload) > req.Out.budget {
		return 0, false, nil
	}
	n, err := req.Out.write(path.Join(rel, name), payload)
	if err != nil {
		return 0, false, err
	}
	return n, true, nil
}

// writeIndex writes _index.json last, so it describes what is actually there,
// and outside the budget, so it is written at all. The budget is what produces
// the truncation the index has to report; letting it refuse the report as well
// would leave query texts and execution plans on disk with nothing naming them.
func writeIndex(req WriteRequest, rel string, idx detailIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("query-store-detail: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.writeUnbudgeted(path.Join(rel, "_index.json"), b)
}

// profiledPlan is one row of the profiled-plan directory's index: the query
// and plan it names, the file it was written to (empty when it was not), and
// how the match was made.
type profiledPlan struct {
	QueryID int64 `json:"query_id"`
	PlanID  int64 `json:"plan_id"`
	// Match names how this row was found, never asserting that it is right.
	// The join key is query_plan_hash, an MD5, so two distinct plans can
	// collide on it — a fact the reader needs beside the plan, not buried in
	// a design document nobody re-reads while triaging an incident.
	Match string `json:"match"`
	// Candidates counts the cached plans sharing THIS plan's query_plan_hash
	// — the partition is the Query Store plan, not the query. 1 means this
	// plan's match was unambiguous; it says nothing about the query's other
	// plans, each of which carries its own count. Above 1 is not a red flag
	// by itself: a bare recompile adds a plan_id to the same hash.
	Candidates int64 `json:"candidates"`
	// LastExecution is when the cached plan last ran, rendered by the corpus
	// encoder like every other timestamp in the archive. It is the field that
	// says whether this plan is the one from the incident being investigated:
	// a profiled plan is pulled from a live cache, and one that last ran three
	// weeks ago answers a different question from one that ran a minute ago.
	//
	// It carries NO OFFSET. The source is sys.dm_exec_query_stats, whose
	// last_execution_time is a datetime, so this is server local time and the
	// encoder deliberately renders it without a Z rather than asserting a UTC
	// conversion that never happened. 021's plan.last_execution comes from the
	// Query Store, which stores datetimeoffset, and does carry one — the two
	// are not directly comparable without the server's offset, which the run's
	// probe records.
	LastExecution json.RawMessage `json:"last_execution,omitempty"`
	PlanFile      string          `json:"plan_file,omitempty"`
}

// profiledIndex is the profiled-plan directory's table of contents. It always
// carries the four fields a reader needs to judge a zero without going back
// to the server: how many queries were asked for, how many came back matched,
// and whether Query Store Plan Stats was even on for this database — a
// database with the feature off and a database with nothing cached both
// produce zero plans, and only this field tells them apart.
type profiledIndex struct {
	Database           string `json:"database"`
	Script             string `json:"script"`
	RequestedQueries   int64  `json:"requested_queries"`
	MatchedPlans       int64  `json:"matched_plans"`
	LastQueryPlanStats string `json:"last_query_plan_stats"`
	// Reason is set only for a SKIP: the detail writer left this database
	// nothing to match against. Empty otherwise, so an ordinary run's index
	// does not carry a field that has nothing to say.
	Reason    string         `json:"reason,omitempty"`
	Plans     []profiledPlan `json:"plans"`
	Omissions []omission     `json:"omissions"`
}

// writeQueryStoreProfiled turns 022's two result sets into the last actual
// execution plan for each query 021 already selected. It is a bonus writer:
// sys.dm_exec_query_plan_stats needs SQL Server 2019+, the feature switched
// on, and the plan still resident in cache, and any one of those being false
// is the ordinary case, not a fault — so nothing here ever fails the run.
func writeQueryStoreProfiled(req WriteRequest) (WriteResult, error) {
	root, ok := setByName(req.Sets, "root")
	if !ok {
		return WriteResult{}, fmt.Errorf("query-store-profiled: result set %q is missing", "root")
	}
	plans, ok := setByName(req.Sets, "profiled")
	if !ok {
		return WriteResult{}, fmt.Errorf("query-store-profiled: result set %q is missing", "profiled")
	}

	rel := path.Join(req.Script.Dir, req.Unit.Folder, req.Script.Base)
	res := WriteResult{Rel: rel}

	requested, _ := int64At(root, 0, "requested_queries")
	matched, _ := int64At(root, 0, "matched_plans")
	lastStat, _ := stringAt(root, 0, "last_query_plan_stats")

	idx := profiledIndex{
		Database:           req.Unit.Name,
		Script:             req.Script.Path,
		RequestedQueries:   requested,
		MatchedPlans:       matched,
		LastQueryPlanStats: lastStat,
		Plans:              []profiledPlan{},
		Omissions:          []omission{},
	}

	ids := req.State.Selected[req.Unit.Name]
	// 021 and 022 run independently, and the second has nothing to match
	// against when the first selected nothing here — Query Store off, the
	// budget exhausted before 021 ran, or simply an empty database. That is
	// a SKIP recorded in this writer's own index, not an error borrowed from
	// a script this one never depends on.
	if len(ids) == 0 {
		idx.Reason = "the detail writer selected no queries for this database"
		n, err := writeProfiledIndex(req, rel, idx)
		res.Bytes += n
		return res, err
	}

	// record puts an omission in this directory's index; omit also raises it in
	// the manifest. They are separate because not every omission here is worth
	// an operator's attention — see the no-match case below.
	record := func(queryID, planID int64, file, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, omission{QueryID: queryID, PlanID: planID, File: file, Reason: reason, Bytes: size})
	}
	omit := func(queryID, planID int64, file, reason string, size int64) {
		record(queryID, planID, file, reason, size)
		req.Warn(fmt.Sprintf("%s: query %d, plan %d: %s", req.Unit.Name, queryID, planID, reason))
	}

	// fail writes the index before a write error leaves this writer, for the
	// reason the detail writer's own fail states: plans already on disk with no
	// index over them is the undescribed directory _index.json exists to
	// prevent. The index's own error is warned rather than returned — the
	// original is the cause, this one only its consequence.
	fail := func(entry profiledPlan, cause error) (WriteResult, error) {
		// The plan this entry names was not written, so it names no file; what
		// is known about it is still worth carrying.
		entry.PlanFile = ""
		idx.Plans = append(idx.Plans, entry)
		n, err := writeProfiledIndex(req, rel, idx)
		res.Bytes += n
		if err != nil {
			req.Warn(fmt.Sprintf("%s: %v", req.Unit.Name, err))
		}
		return res, cause
	}

	for _, id := range ids {
		rows := rowsWhere(plans, "query_id", id)
		if len(rows.Rows) == 0 {
			// The four conditions the DMF needs are each independently unmet
			// more often than not — not opt-in, the plan aged out of cache,
			// past the 128-level nesting limit, or no hash match at all. All
			// four read the same from here: nothing came back. One reason
			// covers them, because distinguishing them is a diagnosis this
			// writer has no evidence to make.
			//
			// RECORDED, NOT WARNED. This file's header states that any of the
			// four being false is the ordinary case and not a fault, and
			// LAST_QUERY_PLAN_STATS is off by default: twenty databases at the
			// default cap of fifty would put a thousand identical lines in the
			// manifest, restating per query what root.last_query_plan_stats
			// already says once per database. The index still carries every
			// one of them, which is where a reader asking about a specific
			// query looks.
			record(id, 0, "", "no profiled plan matched this query_id by query_plan_hash", 0)
			continue
		}

		// Every row, not just the first. The result set's grain is one row per
		// (query_id, plan_id): a query with three Query Store plans still in
		// cache produces three DISTINCT plans, and they are three different
		// answers to "what does this query actually do". Writing one and
		// calling the rest extra candidates would discard real plans under a
		// reason describing something else entirely — the collision case is one
		// plan_id with several cached plan_handles, and that is what
		// `candidates` counts, entirely inside the SQL.
		//
		// This also makes 022 agree with 021, which has always written one file
		// per plan for a query.
		seenPlan := map[int64]bool{}
		for r := range rows.Rows {
			planID, _ := int64At(rows, r, "plan_id")
			if seenPlan[planID] {
				// The SQL keeps one cached plan per plan_id, so this cannot
				// happen unless that ranking is edited away. Named rather than
				// dropped silently, because a repeated plan_id would otherwise
				// overwrite a file and count it twice.
				omit(id, planID, "", "a further row was returned for this plan_id and was not written", 0)
				continue
			}
			seenPlan[planID] = true

			match, _ := stringAt(rows, r, "match")
			candidates, _ := int64At(rows, r, "candidates")
			plan, present := stringAt(rows, r, "query_plan")
			size, _ := int64At(rows, r, "query_plan_bytes")

			entry := profiledPlan{
				QueryID: id, PlanID: planID, Match: match, Candidates: candidates,
				LastExecution: encodedAt(rows, r, "last_execution_time", func(msg string) {
					req.Warn(fmt.Sprintf("%s: query %d, plan %d: %s", req.Unit.Name, id, planID, msg))
				}),
			}

			name := planFileName(id, planID, ".actual")
			switch {
			case !present && size > maxPlanBytes:
				omit(id, planID, name, fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap and was not sent", size, maxPlanBytes), size)
			case !present:
				// A single-operator plan for a trivial query and a NULL past the
				// 128-nesting-level ceiling are indistinguishable from here: the
				// DMF does not say which happened, so neither is claimed.
				omit(id, planID, name, "no plan XML returned for this plan_id", 0)
			case len(plan) > maxPlanBytes:
				omit(id, planID, name, fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap", len(plan), maxPlanBytes), int64(len(plan)))
			default:
				n, ok, err := writeFile(req, rel, name, []byte(plan))
				if err != nil {
					return fail(entry, err)
				}
				if !ok {
					omit(id, planID, name, budgetReason(req.Out.budget), int64(len(plan)))
				} else {
					res.Bytes += n
					res.PlanFiles++
					entry.PlanFile = name
				}
			}

			// The entry goes in WHATEVER happened to the file, which is why
			// PlanFile is omitempty: match, candidates and last_execution are
			// what a reader needs most for a plan that was not written, and a
			// row dropped from plans takes them with it. The omission beside it
			// says why there is no file; it does not say when the cached plan
			// last ran or how many plans shared its hash, and it is not the
			// place to repeat that.
			idx.Plans = append(idx.Plans, entry)
		}
	}

	n, err := writeProfiledIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// writeProfiledIndex writes _index.json outside the budget, exactly like the
// detail writer's: it is what tells a reader "a query with no match" apart
// from "the writer never ran here", so the budget must never be the reason it
// is missing.
func writeProfiledIndex(req WriteRequest, rel string, idx profiledIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("query-store-profiled: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.writeUnbudgeted(path.Join(rel, "_index.json"), b)
}

// encodedAt renders one cell with the corpus encoder, for the same reason
// indexHeader does it for a whole result set: a timestamp in _index.json has to
// be formatted by the code that formats every other timestamp in the archive,
// not by a second convention that drifts from it. It returns nil for an absent
// or NULL cell, which the omitempty on the field then drops — a plan whose last
// execution time the DMF did not report must not carry a zero one.
//
// warn takes the encoder's own warning, for the same reason every other
// omission in this file reaches the manifest: the encoder's warnings name a
// value it could not render faithfully, and one raised while writing an index
// nobody re-encodes would otherwise be raised to no one.
func encodedAt(s ResultSet, row int, col string, warn func(string)) json.RawMessage {
	v, ok := valueAt(s, row, col)
	if !ok || v == nil {
		return nil
	}
	sqlType := ""
	if i := colIndex(s, col); i >= 0 && i < len(s.Types) {
		sqlType = strings.ToUpper(s.Types[i])
	}
	raw, warning := encodeValue(v, sqlType)
	if warning != "" && warn != nil {
		warn(fmt.Sprintf("column %s: %s", col, warning))
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// indexHeader borrows the corpus encoder for the state and window blocks, so
// the timestamps in _index.json are rendered by the same code as every other
// DATETIME2 in the archive rather than by a second, drifting convention here.
//
// warn takes the encoder's warnings for the reason encodedAt states: one
// raised while writing an index nobody re-encodes would otherwise be raised to
// no one. The row encoded here holds the four datetimeoffset window bounds,
// which are exactly the values the encoder has something to say about.
func indexHeader(root ResultSet, warn func(string)) (state, window json.RawMessage, err error) {
	doc, warns, err := Encode([]NamedResultSet{
		{Spec: ResultSpec{Name: RootSetName, Shape: ShapeObject}, Set: root},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("query-store-detail: encoding the index header: %w", err)
	}
	for _, w := range warns {
		if warn != nil {
			warn(w)
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, nil, fmt.Errorf("query-store-detail: reading back the index header: %w", err)
	}
	null := json.RawMessage("null")
	state, window = fields["state"], fields["window"]
	if state == nil {
		state = null
	}
	if window == nil {
		window = null
	}
	return state, window, nil
}
