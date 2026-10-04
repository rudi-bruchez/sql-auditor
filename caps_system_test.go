package sqlauditor_test

import (
	"regexp"
	"testing"

	sqlauditor "github.com/rudi-bruchez/sql-auditor"
	"github.com/rudi-bruchez/sql-auditor/collect"
)

// collectorCode is a collector's text with its comments stripped, so that a
// pattern below matches code and never the header that explains it.
func collectorCode(t *testing.T, path string) string {
	t.Helper()
	b, err := sqlauditor.Queries.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return collect.StripSQLComments(string(b))
}

// The caps that stay must say when they were reached, and the caps that went
// must not come back unannounced. None of these can be exercised by the CI
// collection: it takes no backups past two thousand, does not pass
// --include-default-trace, has no distributor and no deadlock. So the shape
// of each is held here, on the text, the way the other corpus tests hold
// theirs. Each pattern names the line whose removal would make the cut silent
// again.
func TestSystemCapsAreReportedOrGone(t *testing.T) {
	for _, c := range []struct {
		path, why string
		want      []string
		forbid    []string
	}{
		{
			path: "queries/60.backup/010.history.sql",
			why: "recent lists at most @recent_cap backups and the root says when " +
				"the window held more; a virtual device has no path to group on, " +
				"and a path is cut at its last separator of either kind",
			want: []string{
				`SELECT TOP \(@recent_cap\)\s+bs\.database_name`,
				`@recent_cap\s+AS \[window\.recent_cap\]`,
				`WHERE backup_finish_date >= @since\) > @recent_cap\s+THEN 1 ELSE 0 END\s+AS \[window\.recent_capped\]`,
				`WHEN bmf\.device_type IN \(7, 107\) THEN NULL`,
				`PATINDEX\('%\[\\/\]%', REVERSE\(bmf\.physical_device_name\)\)`,
				`GROUP BY bmf\.device_type, d\.\[path\]`,
			},
			forbid: []string{`SELECT TOP \(200\)\s+bs\.database_name`},
		},
		{
			path: "queries/10.system/045.default-trace-detail.sql",
			why: "the 5000 rows are shared out round robin by event class, and the " +
				"root and classes say what each class held against what was kept",
			want: []string{
				`ROW_NUMBER\(\) OVER \(PARTITION BY g\.EventClass\s+ORDER BY g\.StartTime DESC\) AS class_rank`,
				`COUNT\(\*\) OVER \(PARTITION BY g\.EventClass\) AS class_total`,
				`ORDER BY r\.class_rank, r\.StartTime DESC`,
				`HAVING COUNT\(\*\) < MAX\(e\.\[ClassTotal\]\)\)\s+THEN 1 ELSE 0 END\s+AS \[capped\]`,
				`AS \[counts\.events_in_files\]`,
				`MAX\(e\.\[ClassTotal\]\)\s+AS \[held\]`,
			},
			forbid: []string{`ORDER BY g\.StartTime DESC\s+OPTION`},
		},
		{
			path: "queries/10.system/040.error-log.sql",
			why: "notable reports its cap and what matched before it, and keeps the " +
				"newest lines when cut",
			want: []string{
				`200\s+AS \[notable_cap\]`,
				`\(SELECT COUNT\(\*\) FROM #notable\)\s+AS \[notable_matched\]`,
				`SELECT TOP \(200\)\s+n\.LogDate[\s\S]*?FROM #notable AS n\s+ORDER BY n\.LogDate DESC`,
				`200\s+AS \[top_messages_kept\]`,
				`SELECT TOP \(200\)\s+LEFT\(l\.Txt, 80\)`,
			},
		},
		{
			path: "queries/90.availability/042.replication-distribution.sql",
			why:  "repl_errors lists 50 and the root counts every error of the window",
			want: []string{
				`SELECT TOP \(50\) e\.id[^']*COUNT\(\*\) OVER \(\)\s+FROM dbo\.MSrepl_errors`,
				`ISNULL\(\(SELECT MAX\(\[in_window\]\) FROM @errs\), 0\)\s+END\s+AS \[counts\.repl_errors_in_window\]`,
			},
		},
		{
			path:   "queries/20.databases/025.fragmentation.sql",
			why:    "the output is bounded by the 100 partitions measured, not by a TOP",
			forbid: []string{`SELECT TOP \(\d+\) g\.\[table\]`},
		},
		{
			path:   "queries/10.system/060.system-health.sql",
			why:    "every deadlock timestamp the ring holds is listed",
			forbid: []string{`SELECT TOP \(\d+\)\s+CONVERT\(varchar\(23\), event_time, 126\)`},
		},
		{
			path:   "queries/20.databases/028.change-tracking.sql",
			why:    "every internal table holding a page is listed",
			forbid: []string{`SELECT TOP \(\d+\)\s+it\.internal_type_desc`},
		},
	} {
		code := collectorCode(t, c.path)
		for _, p := range c.want {
			if !regexp.MustCompile(p).MatchString(code) {
				t.Errorf("%s no longer matches %s\nwhat it holds: %s", c.path, p, c.why)
			}
		}
		for _, p := range c.forbid {
			if regexp.MustCompile(p).MatchString(code) {
				t.Errorf("%s matches %s again\nwhat it holds: %s", c.path, p, c.why)
			}
		}
	}
}
