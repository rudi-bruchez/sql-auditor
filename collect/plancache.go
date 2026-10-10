package collect

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// The plan-cache-plans writer turns 80.workload/041.plan-cache-plans.sql into
// one .sqlplan file per plan plus an index. Like the deadlock writer, it exists
// because the artifact a reader wants is a file they can open — SSMS renders a
// .sqlplan as the graphical plan — and not a blob of XML inside a JSON array.
//
// It is the collector of last resort for plan shapes: when the Query Store is
// off, nothing else in this corpus keeps a plan at all, and the analysis is
// left with aggregate counters and no way to see why a statement is expensive.

// maxPlanCachePlans bounds how many plans are written. The heaviest by CPU come
// first, and the SQL has already narrowed the cache to what matters by any of
// four definitions of mattering.
//
// It bounds the FILES, not the rows. Every selected plan still arrives with its
// rank, its statistics and its size, and the ones past the cap are named in the
// omissions: the statistics are what make the shape of the workload visible,
// and dropping the rows would erase it while keeping its worst example.
//
// The SAME NUMBER is a literal in 041, and
// TestPlanCacheCapsAreTheSameNumbersInTheCorpus fails on any drift, for the
// reason the deadlock and module caps have the same test: a Go constant raised
// above a stale SQL literal would make plan 201 arrive NULL and be reported as
// a plan the cache does not hold — a false fact about the server.
const maxPlanCachePlans = 200

// maxPlanCachePerMetric is how deep each of the four rankings reaches in 041 —
// total CPU, total duration, total reads and execution count. Four times this
// is maxPlanCachePlans, and a plan ranking well on several metrics is selected
// once, so the two hundred is a ceiling rather than a quota. It is carried into
// the index because "the top 50 by four measures" and "the top 200 by CPU" are
// different selections and a reader comparing two archives has to know which.
//
// It was 25 until 4 October 2026, when three real collections had selected
// 41, 41 and 56 plans with it (2.6 to 4.6 MB); fifty puts them near 5 to 9 MB,
// and maxPlanCachePlans moved with it to stay four times this.
const maxPlanCachePerMetric = 50

// maxPlanCacheBytes bounds one plan. A plan for an ordinary statement is a few
// kilobytes; one reaching four megabytes belongs to a statement with hundreds
// of operators, and it is the archive rather than the reader that has to be
// protected from it.
//
// 041 applies the same number, so an oversized plan arrives as a NULL with its
// true size beside it. That is what lets the index say "too large to collect"
// rather than "the cache holds no plan for this", which are different facts
// about the server.
const maxPlanCacheBytes = 4 << 20

// planCacheIndex is the directory's table of contents. Written even when
// nothing else is: an empty directory and an absent one are different facts,
// and only the index says which happened.
//
// No database and no server name here. The script is instance-scoped — the plan
// cache is an instance-wide structure — so runUnit hands the writer an empty
// DatabaseFolder, and a "server" key filled from it would read as the empty
// string rather than as absent. MANIFEST.txt names the instance once, for the
// whole archive.
type planCacheIndex struct {
	Script string          `json:"script"`
	Counts planCacheCounts `json:"counts"`
	// Caveats travel with the files rather than living only in the SQL header,
	// because the person reading a .sqlplan six months from now has the
	// directory and not the corpus.
	Caveats   []string          `json:"caveats"`
	Plans     []indexedPlan     `json:"plans"`
	Omissions []planOmission    `json:"omissions"`
	Cache     planCacheSnapshot `json:"cache"`
}

// planCacheCounts reports what was selected against what is on disk.
type planCacheCounts struct {
	Selected int `json:"selected"`
	Written  int `json:"written"`
	// NullPlans counts the rows where sys.dm_exec_query_plan returned nothing
	// for a plan that IS in the cache. It is reported rather than folded into
	// the omissions total because its cause is the engine's, not this tool's.
	NullPlans int `json:"null_plans"`
	// WrittenWithoutQueryPlan counts the files written whose XML holds no
	// QueryPlan element: they are counted in Written, and are not plans.
	WrittenWithoutQueryPlan int `json:"written_without_query_plan"`
	CapPlans                int `json:"cap_plans"`
	CapBytes                int `json:"cap_plan_bytes"`
	CapPerRank              int `json:"cap_per_metric"`
}

// planCacheSnapshot is the window the cache itself covered. Without it a reader
// cannot tell a hundred plans gathered over three weeks from a hundred gathered
// since a restart nine minutes ago, and only the second is a reason to distrust
// the ranking.
type planCacheSnapshot struct {
	Statements      int64  `json:"statements"`
	Plans           int64  `json:"plans"`
	OldestPlan      string `json:"oldest_plan,omitempty"`
	NewestExecution string `json:"newest_execution,omitempty"`
}

type indexedPlan struct {
	Rank       int64  `json:"rank"`
	PlanHandle string `json:"plan_handle"`
	Database   string `json:"database,omitempty"`
	Object     string `json:"object,omitempty"`
	Statements int64  `json:"statements"`
	Executions int64  `json:"execution_count"`
	WorkerTime int64  `json:"total_worker_time_us"`
	Elapsed    int64  `json:"total_elapsed_time_us"`
	Reads      int64  `json:"total_logical_reads"`
	Bytes      int64  `json:"bytes"`
	// File is empty when no plan was written; the reason is in the omissions
	// under the same rank, never inferred from the empty string.
	File string `json:"file,omitempty"`
	// Shape says what the written file actually holds. Present whenever File
	// is, absent otherwise.
	Shape *planShape `json:"shape,omitempty"`
}

// planShape is what a written .sqlplan holds, read back from its XML, because
// a file being there does not mean a plan is.
//
// Found on a client instance in October 2026: the plans ranked first and third
// by CPU, close to 690,000 executions each, were API cursors. Their QueryPlan elements
// sit under StmtCursor/CursorPlan/Operation (PopulateQuery, FetchQuery), with
// no StmtSimple at all, and a plan reader that walks StmtSimple reported
// nothing in them. Further down the same ranking, a dozen files of 870 bytes
// held no QueryPlan whatsoever: "FETCH API_CURSOR0000…" statements, the entry
// each sp_cursorfetch RPC leaves in the cache, whose CursorPlan is empty. Both
// shapes were reproduced on SQL Server 2025 by driving sp_cursorprepexec and
// sp_cursorfetch over RPC. In both cases the file looked like a measurement and
// the analyst found out otherwise by opening it.
//
// READ IN GO, NOT IN 041. An XQuery count in the SQL would run on the audited
// instance, over every selected plan of up to maxPlanCacheBytes, and would add
// the xml-method SET option requirements (error 1934) to a script that has none
// today. The XML is already in this process's memory to be written: a
// streaming decode of at most maxPlanCachePlans documents costs the server
// nothing. The file is UTF-8 — the driver hands the nvarchar over as a Go
// string and writeFile writes its bytes, with no XML declaration — so the
// decoder needs no charset handling.
type planShape struct {
	// QueryPlans counts QueryPlan elements anywhere in the document, nested
	// under a cursor's operations or not. Zero means the file shows no plan
	// shape, whatever its statistics say.
	QueryPlans int `json:"query_plans"`
	// Statements counts statements by element and StatementType, e.g.
	// "StmtCursor:FETCH CURSOR", which is what tells the two cursor cases
	// above apart from an ordinary plan.
	Statements map[string]int `json:"statements"`
	Note       string         `json:"note,omitempty"`
}

const (
	planNoteCursorFetch = "No statement here carries a query plan. This is the cache entry an API " +
		"cursor fetch (sp_cursorfetch) leaves: its CursorPlan is an empty skeleton, and the plan " +
		"that does the work is cached under the cursor's DECLARE CURSOR entry (sp_cursorprepexec " +
		"or sp_cursoropen), another plan handle. The statistics beside it are the fetches' own; " +
		"the file shows no plan shape."
	planNoteNoQueryPlan = "No statement here carries a query plan, so the file shows no plan shape. " +
		"The statistics beside it are real; the file is not a plan of them."
	planNoteCursorNested = "The query plans are nested under StmtCursor/CursorPlan/Operation " +
		"(PopulateQuery, FetchQuery), not under StmtSimple: this is a cursor's plan, and a reader " +
		"that only walks StmtSimple finds nothing in it."
)

// readPlanShape counts the statements and query plans of one showplan document.
func readPlanShape(doc string) *planShape {
	s := &planShape{Statements: map[string]int{}}
	d := xml.NewDecoder(strings.NewReader(doc))
	var err error
	for {
		var tok xml.Token
		if tok, err = d.Token(); err != nil {
			break
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case el.Name.Local == "QueryPlan":
			s.QueryPlans++
		case strings.HasPrefix(el.Name.Local, "Stmt"):
			kind := el.Name.Local
			for _, a := range el.Attr {
				if a.Name.Local == "StatementType" {
					kind += ":" + a.Value
				}
			}
			s.Statements[kind]++
		}
	}
	cursor := false
	for k := range s.Statements {
		cursor = cursor || strings.HasPrefix(k, "StmtCursor")
	}
	switch {
	case err != io.EOF:
		s.Note = fmt.Sprintf("the plan XML could not be parsed (%v); the counts cover what was "+
			"read before the error", err)
	case s.QueryPlans == 0 && s.Statements["StmtCursor:FETCH CURSOR"] > 0:
		s.Note = planNoteCursorFetch
	case s.QueryPlans == 0:
		s.Note = planNoteNoQueryPlan
	case cursor:
		s.Note = planNoteCursorNested
	}
	return s
}

type planOmission struct {
	Rank       int64  `json:"rank"`
	PlanHandle string `json:"plan_handle,omitempty"`
	File       string `json:"file,omitempty"`
	Reason     string `json:"reason"`
	Bytes      int64  `json:"bytes,omitempty"`
}

// planCacheCaveats are the three things 041's header says about itself, carried
// into the archive. They are not decoration: each one is a reading a report can
// get wrong from these files alone, and two of them invert a finding.
var planCacheCaveats = []string{
	"The plan cache is not history. A plan absent from it was evicted, has not run since " +
		"the last restart, or was never compiled — and this collector cannot tell those apart " +
		"from a statement that never ran.",
	"These are compiled plans and carry no runtime statistics. An operator cost is an " +
		"estimate the optimiser made, never a measurement of what happened.",
	"The statistics beside each plan are summed over every statement sharing its plan handle, " +
		"which for a stored procedure is all of them. The statement text kept is the heaviest " +
		"one, not the whole batch.",
}

// writePlanCachePlans is the @writer: plan-cache-plans implementation.
func writePlanCachePlans(req WriteRequest) (WriteResult, error) {
	root, ok := setByName(req.Sets, "root")
	if !ok {
		return WriteResult{}, fmt.Errorf("plan-cache-plans: result set %q is missing", "root")
	}
	plans, ok := setByName(req.Sets, "plans")
	if !ok {
		return WriteResult{}, fmt.Errorf("plan-cache-plans: result set %q is missing", "plans")
	}

	// An instance-scoped script has no database folder, so the directory is the
	// script's own — the same shape ResultRelativePath gives an instance script,
	// one level less deep than the per-database writers.
	rel := path.Join(req.Script.Dir, req.Script.Base)
	res := WriteResult{Rel: rel}

	statements, _ := int64At(root, 0, "cache.statements")
	cachedPlans, _ := int64At(root, 0, "cache.plans")
	oldest, _ := stringAt(root, 0, "cache.oldest_plan")
	newest, _ := stringAt(root, 0, "cache.newest_execution")

	idx := planCacheIndex{
		Script: req.Script.Path,
		Counts: planCacheCounts{
			Selected:   len(plans.Rows),
			CapPlans:   maxPlanCachePlans,
			CapBytes:   maxPlanCacheBytes,
			CapPerRank: maxPlanCachePerMetric,
		},
		Caveats:   planCacheCaveats,
		Plans:     []indexedPlan{},
		Omissions: []planOmission{},
		Cache: planCacheSnapshot{
			Statements:      statements,
			Plans:           cachedPlans,
			OldestPlan:      oldest,
			NewestExecution: newest,
		},
	}

	omit := func(rank int64, handle, file, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, planOmission{
			Rank: rank, PlanHandle: handle, File: file, Reason: reason, Bytes: size,
		})
		req.Warn(fmt.Sprintf("%s: plan %d (%s): %s", req.Script.Base, rank, handle, reason))
	}

	fail := func(cause error) (WriteResult, error) {
		n, err := writePlanCacheIndex(req, rel, idx)
		res.Bytes += n
		if err != nil {
			req.Warn(fmt.Sprintf("%s: %v", req.Script.Base, err))
		}
		return res, cause
	}

	for r := range plans.Rows {
		rank, haveRank := int64At(plans, r, "plan.rank")
		handle, _ := stringAt(plans, r, "plan_handle")
		size, _ := int64At(plans, r, "plan_bytes")
		xml, present := stringAt(plans, r, "query_plan")

		entry := indexedPlan{Rank: rank, PlanHandle: handle, Bytes: size}
		entry.Database, _ = stringAt(plans, r, "database_name")
		entry.Object, _ = stringAt(plans, r, "object_name")
		entry.Statements, _ = int64At(plans, r, "statements")
		entry.Executions, _ = int64At(plans, r, "execution_count")
		entry.WorkerTime, _ = int64At(plans, r, "total_worker_time_us")
		entry.Elapsed, _ = int64At(plans, r, "total_elapsed_time_us")
		entry.Reads, _ = int64At(plans, r, "total_logical_reads")

		// Sequential, and by rank rather than by object: a plan handle is 130
		// characters of hex and an object name can be absent, duplicated across
		// schemas, or absent because the batch was ad hoc. The index carries
		// both.
		file := fmt.Sprintf("plan_%03d.sqlplan", rank)

		// FOUR reasons a plan is absent, and the order matters. The count cap
		// is tested FIRST and is guarded by haveRank, exactly as the deadlock
		// cap is: a result set without the column must fall through to the
		// reasons below rather than read a missing rank as 0. Reporting a
		// capped plan as one the cache does not hold would be a false fact
		// about the server.
		switch {
		case haveRank && rank > maxPlanCachePlans:
			omit(rank, handle, "", fmt.Sprintf(
				"plan %d of %d selected: beyond the cap of %d plans, so the plan was not "+
					"collected; the heaviest by CPU were kept",
				rank, idx.Counts.Selected, maxPlanCachePlans), size)
		case size > maxPlanCacheBytes:
			omit(rank, handle, "", fmt.Sprintf(
				"the plan is %d bytes, above the %d byte cap, so it was not collected",
				size, maxPlanCacheBytes), size)
		case !present:
			// The engine's own answer, and the one worth spelling out: the plan
			// IS in the cache, and sys.dm_exec_query_plan declined to render it.
			idx.Counts.NullPlans++
			omit(rank, handle, "", "sys.dm_exec_query_plan returned nothing for this handle: the "+
				"cache holds the plan but could not render it as XML, which happens for a plan "+
				"above the engine's own nesting limit or containing an XML-invalid construct. "+
				"This is not an absent plan", size)
		default:
			n, ok, err := writeFile(req, rel, file, []byte(xml))
			if err != nil {
				idx.Plans = append(idx.Plans, entry)
				return fail(err)
			}
			if !ok {
				omit(rank, handle, file, budgetReason(req.Out.budget), size)
				break
			}
			res.Bytes += n
			res.CachedPlanFiles++
			entry.File = file
			idx.Counts.Written++
			entry.Shape = readPlanShape(xml)
			if entry.Shape.QueryPlans == 0 {
				idx.Counts.WrittenWithoutQueryPlan++
			}
		}
		idx.Plans = append(idx.Plans, entry)
	}

	n, err := writePlanCacheIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// writePlanCacheIndex writes _index.json last, so it describes what is on disk,
// and outside the budget, so it is written at all.
func writePlanCacheIndex(req WriteRequest, rel string, idx planCacheIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("plan-cache-plans: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.writeUnbudgeted(path.Join(rel, "_index.json"), b)
}
