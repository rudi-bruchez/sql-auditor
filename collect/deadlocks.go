package collect

import (
	"encoding/json"
	"fmt"
	"path"
)

// The deadlock-graphs writer turns 10.system/061.deadlock-graphs.sql into one
// .xdl file per deadlock plus an index. Like the module writer, it exists
// because the artifact a reader wants is a file they can open — SSMS renders an
// .xdl as the deadlock diagram — and not a blob of XML inside a JSON array.

// maxDeadlockGraphs bounds how many graphs are written. The most recent are
// kept: a deadlock from four minutes ago is the one somebody is asking about,
// while one from the far end of the ring is history the ring is about to lose
// anyway.
//
// It bounds the FILES, not the rows. Every deadlock still arrives with its
// timestamp, its rank and its size, and the ones past the cap are named in the
// omissions — the timestamps are what make a cluster visible, and dropping the
// rows would erase the shape of the problem while keeping its worst example.
//
// The SAME NUMBER is a literal in 061, and
// TestDeadlockCapsAreTheSameNumbersInTheCorpus fails on any drift, for the
// reason the plan and module caps have the same test: a Go constant raised
// above a stale SQL literal would make graph 101 arrive NULL and be reported as
// a deadlock whose graph the ring does not hold — a false fact about the
// server.
const maxDeadlockGraphs = 100

// maxDeadlockBytes bounds one graph. A deadlock between two ordinary statements
// is a few kilobytes; a graph reaching a megabyte is one with a very deep
// execution stack, and it is the archive rather than the reader that has to be
// protected from it.
const maxDeadlockBytes = 1 << 20

// deadlockIndex is the directory's table of contents. Written even when nothing
// else is: an empty directory and an absent one are different facts, and only
// the index says which happened.
// No database and no server name here, unlike the per-database indexes. This
// script is instance-scoped, so runUnit hands the writer an empty
// DatabaseFolder; a "server" key filled from it would read as the empty string
// rather than as absent. MANIFEST.txt names the instance once, for the whole
// archive, which is where it belongs for a file that describes the instance.
type deadlockIndex struct {
	Script    string            `json:"script"`
	Counts    deadlockCounts    `json:"counts"`
	Deadlocks []indexedDeadlock `json:"deadlocks"`
	Omissions []graphOmission   `json:"omissions"`
}

// deadlockCounts reports what the ring held against what is on disk, plus the
// window. The window is not decoration: "3 deadlocks" means nothing until a
// reader knows whether it covers two days or twenty minutes, and the ring
// buffer is overwritten rather than archived.
type deadlockCounts struct {
	// InRing is the total the SQL found, from BOTH sources together. The two
	// breakdowns beside it say how far back that total reaches: everything from
	// the ring is minutes, everything from the files is days.
	InRing        int    `json:"in_ring"`
	FromRing      int    `json:"from_ring_buffer"`
	FromFile      int    `json:"from_event_file"`
	EventFilePath string `json:"event_file_path,omitempty"`
	// Set when reading the .xel raised. The file is read by the SQL Server
	// service account, not by the connected login, so a path that exists is not
	// necessarily one it may open — and without this the archive would report
	// only what the ring held, silently.
	FileErrorNumber  int    `json:"event_file_error_number,omitempty"`
	FileErrorMessage string `json:"event_file_error_message,omitempty"`
	Written          int    `json:"written"`
	Earliest         string `json:"earliest,omitempty"`
	Latest           string `json:"latest,omitempty"`
	CapGraphs        int    `json:"cap_graphs"`
	CapBytes         int    `json:"cap_graph_bytes"`
}

type indexedDeadlock struct {
	Rank       int64  `json:"rank"`
	OccurredAt string `json:"occurred_at"`
	// Source is "ring_buffer" or "event_file". It matters to anyone comparing
	// two collections: the ring holds minutes and the files hold days, so a run
	// that got everything from the ring covers a window a later run will appear
	// to contradict.
	Source string `json:"source,omitempty"`
	Bytes  int64  `json:"bytes"`
	// File is empty when no graph was written; the reason is in the omissions
	// under the same rank, never inferred from the empty string.
	File string `json:"file,omitempty"`
}

type graphOmission struct {
	Rank       int64  `json:"rank"`
	OccurredAt string `json:"occurred_at"`
	File       string `json:"file,omitempty"`
	Reason     string `json:"reason"`
	Bytes      int64  `json:"bytes,omitempty"`
}

// writeDeadlockGraphs is the @writer: deadlock-graphs implementation.
func writeDeadlockGraphs(req WriteRequest) (WriteResult, error) {
	root, ok := setByName(req.Sets, "root")
	if !ok {
		return WriteResult{}, fmt.Errorf("deadlock-graphs: result set %q is missing", "root")
	}
	deadlocks, ok := setByName(req.Sets, "deadlocks")
	if !ok {
		return WriteResult{}, fmt.Errorf("deadlock-graphs: result set %q is missing", "deadlocks")
	}

	// An instance-scoped script has no database folder, so the directory is the
	// script's own — the same shape ResultRelativePath gives an instance script,
	// one level less deep than the per-database writers.
	rel := path.Join(req.Script.Dir, req.Script.Base)
	res := WriteResult{Rel: rel}

	inRing, _ := int64At(root, 0, "session.deadlocks")
	earliest, _ := stringAt(root, 0, "session.earliest_deadlock")
	latest, _ := stringAt(root, 0, "session.latest_deadlock")
	fromRing, _ := int64At(root, 0, "session.from_ring_buffer")
	fromFile, _ := int64At(root, 0, "session.from_event_file")
	filePath, _ := stringAt(root, 0, "session.event_file_path")
	fileErrNo, _ := int64At(root, 0, "session.event_file_error_number")
	fileErrMsg, _ := stringAt(root, 0, "session.event_file_error_message")
	idx := deadlockIndex{
		Script: req.Script.Path,
		Counts: deadlockCounts{
			InRing:           int(inRing),
			FromRing:         int(fromRing),
			FromFile:         int(fromFile),
			EventFilePath:    filePath,
			FileErrorNumber:  int(fileErrNo),
			FileErrorMessage: fileErrMsg,
			Earliest:         earliest,
			Latest:           latest,
			CapGraphs:        maxDeadlockGraphs,
			CapBytes:         maxDeadlockBytes,
		},
		Deadlocks: []indexedDeadlock{},
		Omissions: []graphOmission{},
	}

	omit := func(rank int64, at, file, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, graphOmission{
			Rank: rank, OccurredAt: at, File: file, Reason: reason, Bytes: size,
		})
		req.Warn(fmt.Sprintf("%s: deadlock %d (%s): %s", req.Script.Base, rank, at, reason))
	}

	fail := func(cause error) (WriteResult, error) {
		n, err := writeDeadlockIndex(req, rel, idx)
		res.Bytes += n
		if err != nil {
			req.Warn(fmt.Sprintf("%s: %v", req.Script.Base, err))
		}
		return res, cause
	}

	for r := range deadlocks.Rows {
		rank, haveRank := int64At(deadlocks, r, "graph.rank")
		at, _ := stringAt(deadlocks, r, "occurred_at")
		size, _ := int64At(deadlocks, r, "graph_bytes")
		graph, present := stringAt(deadlocks, r, "graph")
		source, _ := stringAt(deadlocks, r, "source")

		entry := indexedDeadlock{Rank: rank, OccurredAt: at, Source: source, Bytes: size}
		// Sequential, not timestamped: an XE timestamp carries colons, which
		// are illegal in a Windows filename, and a name sanitised out of its
		// own timestamp is worse than one that never claimed to have it. The
		// index carries the timestamp.
		file := fmt.Sprintf("deadlock_%03d.xdl", rank)

		// THREE reasons a graph is absent. The count cap is tested FIRST and is
		// guarded by haveRank, exactly as the per-query plan cap is: a result
		// set without the column must fall through to the reasons below rather
		// than read a missing rank as 0. Reporting a capped graph as one the
		// ring does not hold would be a false fact about the server.
		switch {
		case haveRank && rank > maxDeadlockGraphs:
			omit(rank, at, "", fmt.Sprintf("deadlock %d of %d in the ring: beyond the cap of %d graphs, so the graph was not collected; the most recent were kept", rank, idx.Counts.InRing, maxDeadlockGraphs), size)
		case size > maxDeadlockBytes:
			omit(rank, at, "", fmt.Sprintf("the graph is %d bytes, above the %d byte cap, so it was not collected", size, maxDeadlockBytes), size)
		case !present:
			omit(rank, at, "", "the ring buffer holds no graph for this event; the deadlock was recorded and its report was not", 0)
		default:
			n, ok, err := writeFile(req, rel, file, []byte(graph))
			if err != nil {
				idx.Deadlocks = append(idx.Deadlocks, entry)
				return fail(err)
			}
			if !ok {
				omit(rank, at, file, budgetReason(req.Out.budget), size)
				break
			}
			res.Bytes += n
			res.GraphFiles++
			entry.File = file
			idx.Counts.Written++
		}
		idx.Deadlocks = append(idx.Deadlocks, entry)
	}

	n, err := writeDeadlockIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// writeDeadlockIndex writes _index.json last, so it describes what is on disk,
// and outside the budget, so it is written at all.
func writeDeadlockIndex(req WriteRequest, rel string, idx deadlockIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("deadlock-graphs: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.writeUnbudgeted(path.Join(rel, "_index.json"), b)
}
