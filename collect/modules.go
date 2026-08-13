package collect

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// The object-definitions writer turns 70.schema/080.modules.sql into one .sql
// file per module plus an index, for the same reason the Query Store writer
// exists: the artifact a reader wants is a file they can open in an editor, and
// a 200 KB procedure body inside a JSON array is not that.
//
// It shares the Query Store writer's machinery deliberately — runWriter,
// writeFile, the budget, the omission idiom — rather than growing a second set
// of conventions for the same problem.

// maxModules bounds how many modules of one database are written. A generated
// schema can carry twelve thousand procedures, and a single database filling
// the run budget would starve every collector after it.
//
// It bounds the FILES, not the rows: every module still arrives with its name,
// its type, its size and its dates, and the ones past the cap are named in the
// omissions. Which ones are kept is decided in the SQL — most recently modified
// first — and the cap is written out there as a literal so a DBA reading the
// exported corpus sees the number. TestModuleCapsAreTheSameNumbersInTheCorpus
// fails on any drift between the two, for the reason the plan caps have the
// same test: a Go constant raised above a stale SQL literal would make module
// 2001 arrive with a NULL definition and be reported as one the catalog does
// not hold, which is a false fact about the server.
const maxModules = 2000

// maxModuleBytes bounds one module's definition. 1 MiB is far above any
// procedure written by hand and below the generated ones that exist to be
// generated. Same duplication in the SQL, same test.
const maxModuleBytes = 1 << 20

// moduleIndex is the directory's table of contents. Like the Query Store's, it
// is written even when nothing else is: an empty directory and an absent one
// are different facts, and only the index can say which happened.
type moduleIndex struct {
	Database  string           `json:"database"`
	Script    string           `json:"script"`
	Counts    moduleCounts     `json:"counts"`
	Modules   []indexedModule  `json:"modules"`
	Omissions []moduleOmission `json:"omissions"`
}

// moduleCounts holds the three populations a reader has to be able to tell
// apart. Total is what the database has, Listed is what the result set carried
// — Total minus nothing, since the SQL filters no row — and Written is how many
// definitions are actually on disk. Written below Listed is normal and the
// omissions say why, one module at a time.
type moduleCounts struct {
	Total   int64 `json:"total"`
	Listed  int   `json:"listed"`
	Written int   `json:"written"`
	// Caps travel with the counts so the index is readable without the corpus
	// beside it.
	CapModules int `json:"cap_modules"`
	CapBytes   int `json:"cap_module_bytes"`
}

type indexedModule struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Rank   int64  `json:"rank"`
	Bytes  int64  `json:"bytes"`
	// File is empty when no definition was written. The reason is then in the
	// omissions under the same schema and name — never inferred from the empty
	// string, which cannot say which of four different things happened.
	File string `json:"file,omitempty"`
}

// moduleOmission is one definition the archive does not contain, and why.
// Schema and Name rather than an id: an object_id means nothing to a reader
// holding only the archive.
type moduleOmission struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	File   string `json:"file,omitempty"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes,omitempty"`
}

// writeObjectDefinitions is the @writer: object-definitions implementation.
func writeObjectDefinitions(req WriteRequest) (WriteResult, error) {
	root, ok := setByName(req.Sets, "root")
	if !ok {
		return WriteResult{}, fmt.Errorf("object-definitions: result set %q is missing", "root")
	}
	modules, ok := setByName(req.Sets, "modules")
	if !ok {
		return WriteResult{}, fmt.Errorf("object-definitions: result set %q is missing", "modules")
	}

	rel := path.Join(req.Script.Dir, req.Unit.Folder, req.Script.Base)
	res := WriteResult{Rel: rel}

	total, _ := int64At(root, 0, "modules_total")
	idx := moduleIndex{
		Database: req.Unit.Name,
		Script:   req.Script.Path,
		Counts: moduleCounts{
			Total:      total,
			Listed:     len(modules.Rows),
			CapModules: maxModules,
			CapBytes:   maxModuleBytes,
		},
		Modules:   []indexedModule{},
		Omissions: []moduleOmission{},
	}

	omit := func(schema, name, file, reason string, size int64) {
		idx.Omissions = append(idx.Omissions, moduleOmission{
			Schema: schema, Name: name, File: file, Reason: reason, Bytes: size,
		})
		req.Warn(fmt.Sprintf("%s: %s.%s: %s", req.Unit.Name, schema, name, reason))
	}

	// The index goes out before any write error leaves this writer, exactly as
	// the Query Store writer does it: by the time one occurs, definitions are
	// already on disk, and returning without an index leaves that directory
	// undescribed. A failure of the index write itself is discarded in favour of
	// the original cause and reported as a warning — the disk that just refused
	// a definition is the same disk refusing the index, and naming the second
	// would send the reader looking in the wrong place.
	fail := func(cause error) (WriteResult, error) {
		n, err := writeModuleIndex(req, rel, idx)
		res.Bytes += n
		if err != nil {
			req.Warn(fmt.Sprintf("%s: %v", req.Unit.Name, err))
		}
		return res, cause
	}

	// Two modules can sanitise to one filename — "rpt/daily" and "rpt daily"
	// both become "rpt_daily" — and writing both would leave one body under the
	// other's name, which is worse than either omission this writer records.
	// Suffixing is the same answer ResolveDatabaseFolders gives for the same
	// problem, and the index carries the name actually used.
	used := map[string]int{}

	for r := range modules.Rows {
		schema, _ := stringAt(modules, r, "schema")
		name, _ := stringAt(modules, r, "name")
		typeDesc, _ := stringAt(modules, r, "type")
		rank, _ := int64At(modules, r, "module.rank")
		size, _ := int64At(modules, r, "definition_bytes")
		encrypted := boolAt(modules, r, "is_encrypted")
		def, present := stringAt(modules, r, "definition")

		entry := indexedModule{Schema: schema, Name: name, Type: typeDesc, Rank: rank, Bytes: size}
		file := moduleFileName(schema, name, used)

		// FOUR reasons a definition is absent, and they are tested in this
		// order because each is more specific than the one after it. Collapsing
		// any two would have the archive state something false about the server:
		// an encrypted module reported as "too large" invents a size nobody
		// measured, and one past the cap reported as "the catalog holds no
		// definition" denies the existence of code that is right there.
		switch {
		case encrypted:
			omit(schema, name, "", "the module is encrypted (WITH ENCRYPTION), so the server returns no definition; nothing was collected and nothing could have been", 0)
		case rank > maxModules:
			omit(schema, name, "", fmt.Sprintf("module %d of %d for this database: beyond the per-database cap of %d modules, so the definition was not collected; the most recently modified were kept", rank, idx.Counts.Total, maxModules), size)
		case size > maxModuleBytes:
			omit(schema, name, "", fmt.Sprintf("the definition is %d bytes, above the %d byte per-module cap, so it was not collected", size, maxModuleBytes), size)
		case !present:
			omit(schema, name, "", "the catalog holds no definition for this object", 0)
		default:
			n, ok, err := writeFile(req, rel, file, []byte(def))
			if err != nil {
				// This module is named in the index without a file, the ones
				// before it keep theirs, and the error propagates.
				idx.Modules = append(idx.Modules, entry)
				return fail(err)
			}
			if !ok {
				omit(schema, name, file, budgetReason(req.Out.budget), size)
				break
			}
			res.Bytes += n
			res.DefinitionFiles++
			entry.File = file
			idx.Counts.Written++
		}
		idx.Modules = append(idx.Modules, entry)
	}

	n, err := writeModuleIndex(req, rel, idx)
	res.Bytes += n
	return res, err
}

// moduleFileName turns a schema and object name into a filename, keeping every
// name distinct. SafeFolderName is reused rather than reimplemented: the rules
// that make a directory name safe on Windows — reserved device names, illegal
// characters, trailing dots — are exactly the rules that make a file name safe
// there.
func moduleFileName(schema, name string, used map[string]int) string {
	stem := SafeFolderName(schema) + "." + SafeFolderName(name)
	key := strings.ToUpper(stem)
	used[key]++
	if n := used[key]; n > 1 {
		stem = fmt.Sprintf("%s~%d", stem, n)
	}
	return stem + ".sql"
}

// writeModuleIndex writes _index.json last, so it describes what is on disk,
// and outside the budget, so it is written at all. The budget is what produces
// the truncation the index reports; letting it refuse the report would leave
// module definitions on disk with nothing naming them.
func writeModuleIndex(req WriteRequest, rel string, idx moduleIndex) (int, error) {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("object-definitions: encoding _index.json: %w", err)
	}
	b = append(b, '\n')
	return req.Out.writeUnbudgeted(path.Join(rel, "_index.json"), b)
}
