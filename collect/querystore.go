package collect

import (
	"encoding/json"
	"fmt"
	"path"
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
type WriteResult struct {
	Rel       string
	Bytes     int
	PlanFiles int
	TextFiles int
}

// ScriptWriter turns materialised result sets into files.
type ScriptWriter func(WriteRequest) (WriteResult, error)

// writerFor resolves an @writer name to its implementation, and returns nil for
// a name it does not know so the caller can refuse the script rather than fall
// back to the encoder and quietly emit a document where a directory was meant.
func writerFor(name string) ScriptWriter {
	switch name {
	case "query-store-detail":
		return writeQueryStoreDetail
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
	QueryID int64  `json:"query_id"`
	PlanID  int64  `json:"plan_id"`
	Reason  string `json:"reason"`
	// Bytes is the plan's size when the server reported one, and 0 when nobody
	// knows it — a plan the Query Store does not hold has no size.
	Bytes int64 `json:"bytes,omitempty"`
}

type indexedQuery struct {
	QueryID int64   `json:"query_id"`
	Plans   []int64 `json:"plans"`
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
	stateRaw, windowRaw, err := indexHeader(root)
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

	// A database whose Query Store is switched off is a skip, not a failure and
	// not an empty success. The index still has to be written, or the analysis
	// layer cannot tell the two apart.
	if state == "OFF" || len(root.Rows) == 0 {
		n, err := writeIndex(req, rel, idx)
		res.Bytes += n
		return res, err
	}

	omit := func(queryID, planID int64, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, omission{QueryID: queryID, PlanID: planID, Reason: reason, Bytes: size})
		req.Warn(fmt.Sprintf("%s: query %d, plan %d: %s", req.Unit.Name, queryID, planID, reason))
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
			Plans:     []int64{},
			Ranks:     map[string]int64{},
			TextFile:  queryFileStem(id) + ".sql",
			StatsFile: queryFileStem(id) + ".stats.json",
			PlanFiles: []string{},
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
		for r := range rows.Rows {
			if s, ok := stringAt(rows, r, "text"); ok {
				text = s
				break
			}
		}
		if n, ok, err := writeFile(req, rel, entry.TextFile, []byte(text)); err != nil {
			return res, err
		} else if ok {
			res.Bytes += n
			res.TextFiles++
		} else {
			omit(id, 0, budgetReason, 0)
			entry.TextFile = ""
		}

		for r := range rows.Rows {
			planID, _ := int64At(rows, r, "plan_id")
			entry.Plans = append(entry.Plans, planID)
			if boolAt(rows, r, "is_forced") {
				entry.Forced = true
				idx.Selection.Forced++
			}

			size, _ := int64At(rows, r, "query_plan_bytes")
			plan, present := stringAt(rows, r, "query_plan")
			switch {
			case !present && size > maxPlanBytes:
				// The SQL's DATALENGTH guard nulled it out before it crossed the
				// wire. Reported apart from a plan that never existed, because
				// conflating them turns a plan we chose not to fetch into one the
				// server does not have.
				omit(id, planID, fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap and was not sent", size, maxPlanBytes), size)
			case !present:
				omit(id, planID, "the Query Store holds no plan XML for this plan_id", 0)
			case len(plan) > maxPlanBytes:
				// The backstop, for when the SQL guard has been edited away.
				omit(id, planID, fmt.Sprintf("plan XML of %d bytes exceeds the %d byte per-plan cap", len(plan), maxPlanBytes), int64(len(plan)))
			default:
				name := planFileName(id, planID, "")
				n, ok, err := writeFile(req, rel, name, []byte(plan))
				if err != nil {
					return res, err
				}
				if !ok {
					omit(id, planID, budgetReason, int64(len(plan)))
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
			return res, fmt.Errorf("query-store-detail: encoding statistics for query %d: %w", id, err)
		}
		for _, w := range warns {
			req.Warn(fmt.Sprintf("%s: query %d: %s", req.Unit.Name, id, w))
		}
		if n, ok, err := writeFile(req, rel, entry.StatsFile, stats); err != nil {
			return res, err
		} else if ok {
			res.Bytes += n
		} else {
			omit(id, 0, budgetReason, int64(len(stats)))
			entry.StatsFile = ""
		}

		idx.Queries = append(idx.Queries, entry)
	}

	idx.Selection.Ranked = len(idx.Queries)
	req.State.Selected[req.Unit.Name] = ids

	n, err := writeIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// budgetReason is the one sentence for a refusal by the run budget. It names
// the cap rather than saying "too large", because the file itself may be small
// and the reader would otherwise look for the fault in the wrong place.
const budgetReason = "the run reached the 256 MiB extraction cap"

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

// writeIndex writes _index.json last, so it describes what is actually there.
// It goes through the budget check like everything else, but its refusal is an
// error: a directory without its index cannot be read at all.
func writeIndex(req WriteRequest, rel string, idx detailIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("query-store-detail: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.write(path.Join(rel, "_index.json"), b)
}

// indexHeader borrows the corpus encoder for the state and window blocks, so
// the timestamps in _index.json are rendered by the same code as every other
// DATETIME2 in the archive rather than by a second, drifting convention here.
func indexHeader(root ResultSet) (state, window json.RawMessage, err error) {
	doc, _, err := Encode([]NamedResultSet{
		{Spec: ResultSpec{Name: RootSetName, Shape: ShapeObject}, Set: root},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("query-store-detail: encoding the index header: %w", err)
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
