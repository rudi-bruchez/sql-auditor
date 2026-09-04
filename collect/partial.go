package collect

import (
	"fmt"
	"sort"
	"strings"
)

// The guard pattern, seen from the pipeline.
//
// Four collectors — 20.databases/020.properties, 70.schema/010.objects,
// 70.schema/020.index-usage and 70.schema/030.index-operational — read each of
// their areas inside its own TRY/CATCH and emit every result set whatever
// happened, because they name user objects and so wait behind any ALTER on
// them. Before that, one blocked read cost the whole document; the four were
// measured losing everything behind a single open ALTER TABLE.
//
// The trade is right, and it has a cost that must not be paid silently. What
// the pipeline used to see as an error it now sees as a collector that
// succeeded: the batch ran, every result set arrived, the file was written. A
// run that lost half a database's schema would print "0 error(s)" and exit 0,
// and the only record would be inside a JSON file nobody opens until the
// analysis stage.
//
// So the pipeline reads the answer back out of the document it just built. The
// convention is the collector's rather than this package's: a root column named
// errors.<area> carrying a non-zero number means that area did not collect. A
// collector that does not use the pattern has no such column and reports
// nothing, which is why this needs no list of file names.

// reportedErrors returns the areas a collector said it could not read, mapped
// to the SQL error number it met, and how many areas it declared in all. It
// reads only the root result set, and only columns prefixed "errors.".
//
// The total is returned because the failing names alone cannot be phrased
// honestly: three blocked areas out of three is not "everything except three",
// it is a collector that got nothing, and the warning has to be able to say
// which of the two happened.
func reportedErrors(sets []NamedResultSet) (map[string]int64, int) {
	out := map[string]int64{}
	total := 0
	for _, ns := range sets {
		if ns.Spec.Name != RootSetName || len(ns.Set.Rows) == 0 {
			continue
		}
		row := ns.Set.Rows[0]
		for i, col := range ns.Set.Columns {
			area, ok := strings.CutPrefix(col, "errors.")
			if !ok || i >= len(row) {
				continue
			}
			total++
			// Zero is the collector saying "this area was fine", which is the
			// overwhelmingly common case and not worth a word. A NULL or a
			// non-numeric value is a collector that does not mean what this
			// convention means, and is ignored rather than guessed at.
			if n, ok := asInt64(row[i]); ok && n != 0 {
				out[area] = n
			}
		}
	}
	return out, total
}

// partialWarning is the sentence MANIFEST.txt and the operator both get. It
// names the collector, the database, every area and its error number, because
// the reader deciding whether the archive is usable needs all four and the
// number is what tells a lock timeout from a permission refusal.
func partialWarning(script, target string, areas map[string]int64, total int) string {
	names := make([]string, 0, len(areas))
	for a := range areas {
		names = append(names, a)
	}
	// Sorted, so a degraded collection produces the same manifest twice
	// running rather than a diff that is only Go's map iteration order.
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, a := range names {
		parts = append(parts, fmt.Sprintf("%s (error %d)", a, areas[a]))
	}
	where := script
	if target != "" {
		where = script + " on " + target
	}
	// The closing sentence is the one a reader needs and it changes with the
	// count: a document whose every area was refused is not a partial result
	// to be read around, it is a file that holds nothing but its own excuse.
	rest := "The rest of the document is complete"
	if len(areas) == total {
		rest = "Nothing else was read either — every area of this collector was refused"
	}
	return fmt.Sprintf("%s: %d of its %d areas did not return — %s. The document was "+
		"written. %s, and those areas are empty because the read did not come back, "+
		"not because there is nothing there.",
		where, len(areas), total, strings.Join(parts, ", "), rest)
}
