package collect

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 80.workload/043 against a real instance, through runUnit, the path a
// collection takes. Skipped unless SQL_AUDITOR_LIVE_SERVER is set
// (liveConfig):
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run '^TestLiveQueryStoreParallelCost' -v
//
// Every database here is created under the ZzAvgDopLive prefix, with a
// suffix of its own so two runs cannot collide, and dropped by t.Cleanup. The
// oracles read the test database from master with three-part names, so that
// their own reads are not captured into the store they check.

// pcRootKeys and pcBandKeys are the lists of "The collector" in
// docs/query-store-parallel-cost-spec.md: the whole of what 043 may emit.
var pcRootKeys = []string{
	"database", "collected_at",
	"state.actual", "state.capture_mode", "state.interval_minutes", "state.not_read_because",
	"schedulers",
	"window.days", "window.from", "window.oldest_interval", "window.newest_interval",
	"window.store_oldest_interval", "window.intervals", "window.runtime_rows",
	"window.executions", "window.cpu_s", "window.scalar_function_cpu_s",
	"window.parallel_plans", "window.parallel_plan_cpu_s",
	"window.parallel_executions", "window.parallel_cpu_s",
	"window.parallel_executions_min", "window.parallel_cpu_s_min",
	"window.serial_plans_dop_above_1", "window.dop_above_schedulers",
	"nested.parallel_plans", "nested.parallel_cpu_s",
	"excluded.other_replicas_executions", "excluded.other_replicas_cpu_s",
	"cap", "chunk", "budget.bytes", "budget.ms",
	"selection.duration_ms",
	"examined.plans", "examined.plans_read", "examined.bytes_read", "examined.duration_ms",
	"examined.largest_plan_bytes", "examined.share_of_parallel_cpu_pct", "examined.stopped_by",
	"truncated",
	"unknown.no_plan", "unknown.no_cost",
}

var pcBandKeys = []string{
	"band", "cost_from", "cost_to", "statements", "parallel_statements",
	"executions", "parallel_executions", "cpu_s", "parallel_cpu_s", "max_dop",
	"parallel_executions_min", "parallel_cpu_s_min", "avg_dop",
}

// pcBandBounds are 042's boundaries; unknown is the band of a plan without a
// cost.
var pcBandBounds = []struct {
	name     string
	from, to float64
}{
	{"lt_5", 0, 5}, {"5_25", 5, 25}, {"25_50", 25, 50}, {"50_100", 50, 100},
	{"100_500", 100, 500}, {"ge_500", 500, math.Inf(1)},
}

// pcSuffix keeps two runs on one instance from creating the same database.
func pcSuffix() string { return strconv.FormatInt(time.Now().UnixNano()%1e9, 36) }

func pcExec(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		t.Fatalf("%v\non: %s", err, stmt)
	}
}

// pcAdmin opens master for the DDL and the oracles.
func pcAdmin(t *testing.T) (*Config, *sql.DB) {
	t.Helper()
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 5 * time.Minute
	admin, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	return cfg, admin
}

// pcCreateDatabase creates a database whose store captures everything in
// one-minute intervals, and drops it when the test ends. collate and
// noAutoStats serve the serial store of TestLiveQueryStoreParallelCostStoresNotRead.
func pcCreateDatabase(t *testing.T, admin *sql.DB, name, collate string, noAutoStats bool) {
	t.Helper()
	q := quoteName(name)
	create := "CREATE DATABASE " + q
	if collate != "" {
		create += " COLLATE " + collate
	}
	pcExec(t, admin, create+";")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "IF DB_ID(@p1) IS NOT NULL BEGIN ALTER DATABASE "+q+
			" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+q+"; END", name); err != nil {
			t.Errorf("dropping %s: %v; drop it by hand", name, err)
		}
	})
	pcExec(t, admin, "ALTER DATABASE "+q+" SET RECOVERY SIMPLE;")
	if noAutoStats {
		pcExec(t, admin, "ALTER DATABASE "+q+" SET AUTO_CREATE_STATISTICS OFF;")
	}
	pcExec(t, admin, "ALTER DATABASE "+q+" SET QUERY_STORE = ON "+
		"(OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = ALL, INTERVAL_LENGTH_MINUTES = 1);")
}

// pcOpen opens a connection pool on the test database, closed at the end of
// the test (before the drop, cleanups running in reverse order).
func pcOpen(t *testing.T, cfg *Config, name string) *sql.DB {
	t.Helper()
	c := *cfg
	c.Database = name
	db, err := Open(&c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// pcLadder builds and runs test 3's workload, as the spec writes it, then
// the plans this plan adds to it (above 25, and nested), and flushes the
// store so the views hold it.
func pcLadder(t *testing.T, cfg *Config, name string) {
	t.Helper()
	w := pcOpen(t, cfg, name)
	sizes := []int{20000, 50000, 100000, 200000, 500000, 1000000, 2000000}
	for i, n := range sizes {
		tb := fmt.Sprintf("dbo.Ladder%d", i+1)
		pcExec(t, w, "CREATE TABLE "+tb+" (id bigint NOT NULL, pad char(100) NOT NULL);")
		pcExec(t, w, fmt.Sprintf("INSERT INTO %s WITH (TABLOCK) (id, pad) "+
			"SELECT TOP (%d) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x' "+
			"FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b CROSS JOIN sys.all_columns AS c;", tb, n))
	}
	for i := range sizes {
		tb := fmt.Sprintf("dbo.Ladder%d", i+1)
		pcExec(t, w, "SELECT COUNT_BIG(*) FROM "+tb+" WHERE id % 7 = 3;")
		pcExec(t, w, "SELECT COUNT_BIG(*) FROM "+tb+" WHERE id % 7 = 3 OPTION (MAXDOP 2);")
	}
	pcExec(t, w, "SELECT COUNT_BIG(*) FROM dbo.Ladder6 WHERE pad <> 'ZZ043_PLANTED_TEXT' OPTION (MAXDOP 2);")
	pcExec(t, w, "CREATE PROCEDURE dbo.TwoStatements AS BEGIN SET NOCOUNT ON; "+
		"SELECT pad FROM dbo.Ladder1 WHERE id = 1; "+
		"SELECT COUNT_BIG(*) FROM dbo.Ladder7 WHERE id % 7 = 4; END;")
	pcExec(t, w, "EXEC dbo.TwoStatements;")
	// Not in the spec's workload. The ladder's parallel plans all cost under
	// 25 on the lab, so no band boundary above 25 separated two plans. These
	// scan the largest heaps joined by UNION ALL, whose cost adds up branch
	// by branch: 34.6, 69.2 and 207.6 in batch mode (compatibility 170), 34.9,
	// 69.8 and 209.3 in row mode (130), one plan in each of 25_50, 50_100 and
	// 100_500 either way, in about 1.7 s together.
	for _, heaps := range [][]string{
		{"Ladder7", "Ladder6"}, {"Ladder7", "Ladder7", "Ladder7"}, slices.Repeat([]string{"Ladder7"}, 9),
	} {
		var branches []string
		for _, h := range heaps {
			branches = append(branches, "SELECT l.id FROM dbo."+h+" AS l")
		}
		pcExec(t, w, "SELECT COUNT_BIG(*) FROM ("+strings.Join(branches, " UNION ALL ")+
			") AS u WHERE u.id % 7 = 3 OPTION (MAXDOP 2);")
	}
	// Nested queries, for nested.* and the scalar function rule of point 7:
	// a trigger whose statement is a parallel aggregate, fired once, and a
	// scalar function whose body runs once per row of its caller, 300 times.
	// The function must not be inlined, or its body is no query of its own;
	// INLINE = OFF exists from SQL Server 2019, before which nothing inlines.
	pcExec(t, w, "CREATE TABLE dbo.Fired (id int NOT NULL);")
	pcExec(t, w, "CREATE TRIGGER dbo.FiredCount ON dbo.Fired AFTER INSERT AS BEGIN SET NOCOUNT ON; "+
		"DECLARE @n bigint; SELECT @n = COUNT_BIG(*) FROM dbo.Ladder6 WHERE id % 7 = 5 OPTION (MAXDOP 2); END;")
	pcExec(t, w, "INSERT INTO dbo.Fired (id) VALUES (1);")
	var major int
	if err := w.QueryRow("SELECT CAST(SERVERPROPERTY('ProductMajorVersion') AS int);").Scan(&major); err != nil {
		t.Fatal(err)
	}
	inline := ""
	if major >= 15 {
		inline = " WITH INLINE = OFF"
	}
	pcExec(t, w, "CREATE FUNCTION dbo.Scalar1 (@x bigint) RETURNS bigint"+inline+" AS BEGIN "+
		"RETURN (SELECT COUNT_BIG(*) FROM dbo.Ladder1 WHERE id = @x); END;")
	pcExec(t, w, "SELECT SUM(dbo.Scalar1(l.id)) FROM dbo.Ladder1 AS l WHERE l.id <= 300;")
	pcExec(t, w, "CREATE TABLE #savings (object_name sysname, schema_name sysname, index_id int, "+
		"partition_number int, size_current_kb bigint, size_requested_kb bigint, "+
		"sample_current_kb bigint, sample_requested_kb bigint); "+
		"INSERT INTO #savings EXEC sys.sp_estimate_data_compression_savings @schema_name = N'dbo', "+
		"@object_name = N'Ladder7', @index_id = NULL, @partition_number = NULL, @data_compression = N'PAGE';")
	pcExec(t, w, "EXEC sys.sp_query_store_flush_db;")
}

// pcRun runs s on database name through runUnit and returns the document it
// wrote, raw and decoded.
func pcRun(t *testing.T, cfg *Config, s Script, name string) ([]byte, map[string]any) {
	t.Helper()
	ctx := context.Background()
	runner, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	conn, err := runner.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dir := t.TempDir()
	m, rw := &Manifest{}, newRunWriter(dir, 1<<20)
	u := DatabaseFolder{Name: name, Folder: "db"}
	if _, err := runUnit(ctx, ctx, conn, Options{Config: cfg}, m, rw, s, u, nil, 0); err != nil {
		t.Fatalf("043 on %s: %v", name, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ResultRelativePath(s.Dir, s.Base, u.Folder))))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

// pcFlat is the document's root as dotted keys, the bands left out.
func pcFlat(doc map[string]any) map[string]any {
	out := map[string]any{}
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			if prefix == "" && k == "bands" {
				continue
			}
			if sub, ok := v.(map[string]any); ok {
				walk(prefix+k+".", sub)
				continue
			}
			out[prefix+k] = v
		}
	}
	walk("", doc)
	return out
}

func pcBands(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	arr, ok := doc["bands"].([]any)
	if !ok {
		t.Fatalf("the document has no bands array: %v", doc["bands"])
	}
	var out []map[string]any
	for _, b := range arr {
		out = append(out, b.(map[string]any))
	}
	return out
}

func pcNum(t *testing.T, v any, what string) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %v (%T), want a number", what, v, v)
	}
	return f
}

// pcPlan is one plan of the test store, read by the oracle.
type pcPlan struct {
	id                                 int64
	parallel, marker                   bool
	cost                               sql.NullFloat64
	costed                             int64
	bytes                              int64
	execs, parRows, parExecs, minExecs int64
	cpu, parCPU, minCPU, dopWeighted   float64
	maxDOP                             int64
	kind                               string // sys.objects.type of the query's object, "" for none
}

// pcOracle reads every plan of the test store executed in the last seven
// days, with its cost through the xml path on the first costed statement
// element, the independent reading the spec asks for, its runtime figures
// summed by the test's own query, and the type of the object its query
// belongs to.
func pcOracle(t *testing.T, admin *sql.DB, name string) []pcPlan {
	t.Helper()
	q := quoteName(name)
	rows, err := admin.Query(`
SELECT p.plan_id, CAST(p.is_parallel_plan AS int),
       CASE WHEN CHARINDEX(N' Parallel="1"', p.query_plan) > 0 THEN 1 ELSE 0 END,
       x.cost, ISNULL(x.costed, 0), ISNULL(DATALENGTH(p.query_plan), 0),
       r.execs, r.par_rows, r.par_execs, r.min_execs, r.cpu, r.par_cpu, r.min_cpu, r.dop_weighted, r.max_dop,
       ISNULL(RTRIM(ob.type), '')
FROM ` + q + `.sys.query_store_plan AS p
JOIN ` + q + `.sys.query_store_query AS qq ON qq.query_id = p.query_id
LEFT JOIN ` + q + `.sys.objects AS ob ON ob.object_id = qq.object_id
JOIN (SELECT rs.plan_id,
             SUM(rs.count_executions) AS execs,
             COUNT_BIG(CASE WHEN rs.max_dop > 1 THEN 1 END) AS par_rows,
             SUM(CASE WHEN rs.max_dop > 1 THEN rs.count_executions ELSE 0 END) AS par_execs,
             SUM(CASE WHEN rs.min_dop > 1 THEN rs.count_executions ELSE 0 END) AS min_execs,
             SUM(rs.avg_cpu_time * rs.count_executions) AS cpu,
             SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END) AS par_cpu,
             SUM(CASE WHEN rs.min_dop > 1 THEN rs.avg_cpu_time * rs.count_executions ELSE 0 END) AS min_cpu,
             SUM(CASE WHEN rs.max_dop > 1 THEN rs.avg_dop * rs.count_executions ELSE 0 END) AS dop_weighted,
             MAX(rs.max_dop) AS max_dop
      FROM ` + q + `.sys.query_store_runtime_stats AS rs
      JOIN ` + q + `.sys.query_store_runtime_stats_interval AS i
        ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
      WHERE i.end_time > DATEADD(day, -7, SYSDATETIMEOFFSET())
      GROUP BY rs.plan_id) AS r ON r.plan_id = p.plan_id
CROSS APPLY (SELECT TRY_CAST(p.query_plan AS xml) AS doc) AS c
CROSS APPLY (SELECT c.doc.value('(//*[@StatementSubTreeCost])[1]/@StatementSubTreeCost', 'float') AS cost,
                    c.doc.value('count(//*[@StatementSubTreeCost])', 'int') AS costed) AS x
OPTION (MAXDOP 1);`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pcPlan
	for rows.Next() {
		var p pcPlan
		var par, marker int
		if err := rows.Scan(&p.id, &par, &marker, &p.cost, &p.costed, &p.bytes, &p.execs, &p.parRows,
			&p.parExecs, &p.minExecs, &p.cpu, &p.parCPU, &p.minCPU, &p.dopWeighted, &p.maxDOP, &p.kind); err != nil {
			t.Fatal(err)
		}
		p.parallel, p.marker = par == 1, marker == 1
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// pcSerialAboveOne counts the serial plans with a runtime row at max_dop
// above 1, the anomaly of point 4 of the spec.
func pcSerialAboveOne(plans []pcPlan) int {
	n := 0
	for _, p := range plans {
		if !p.parallel && p.parRows > 0 {
			n++
		}
	}
	return n
}

// pcParallel returns the parallel plans in 043's rank order: CPU at a degree
// above 1, highest first, then plan id.
func pcParallel(plans []pcPlan) []pcPlan {
	var out []pcPlan
	for _, p := range plans {
		if p.parallel {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].parCPU != out[j].parCPU {
			return out[i].parCPU > out[j].parCPU
		}
		return out[i].id < out[j].id
	})
	return out
}

func pcBandOf(p pcPlan) string {
	if !p.cost.Valid {
		return "unknown"
	}
	for _, b := range pcBandBounds {
		if p.cost.Float64 >= b.from && p.cost.Float64 < b.to {
			return b.name
		}
	}
	return "unknown"
}

// pcSeconds is 043's rounding of microseconds to a decimal(18,1) of seconds.
func pcSeconds(us float64) float64 { return math.Round(us/1e5) / 10 }

// pcCompareBands checks the document's seven bands against the plans that
// should fill them. Counts are exact. CPU figures are rounded per band on
// both sides, so they may differ by the last digit when the sums are taken in
// another order; 0.1 s is that digit.
func pcCompareBands(t *testing.T, doc map[string]any, want []pcPlan) {
	t.Helper()
	type agg struct {
		statements, parStatements, execs, parExecs, minExecs int64
		cpu, parCPU, minCPU, dopWeighted                     float64
		maxDOP                                               int64
	}
	exp := map[string]*agg{}
	for _, p := range want {
		name := pcBandOf(p)
		if exp[name] == nil {
			exp[name] = &agg{maxDOP: -1}
		}
		a := exp[name]
		a.statements++
		if p.parRows > 0 {
			a.parStatements++
		}
		a.execs += p.execs
		a.parExecs += p.parExecs
		a.minExecs += p.minExecs
		a.cpu += p.cpu
		a.parCPU += p.parCPU
		a.minCPU += p.minCPU
		a.dopWeighted += p.dopWeighted
		a.maxDOP = max(a.maxDOP, p.maxDOP)
	}
	bands := pcBands(t, doc)
	var names []string
	for _, b := range bands {
		names = append(names, b["band"].(string))
	}
	if !slices.Equal(names, []string{"lt_5", "5_25", "25_50", "50_100", "100_500", "ge_500", "unknown"}) {
		t.Fatalf("bands %v, want 042's seven in order", names)
	}
	for _, b := range bands {
		name := b["band"].(string)
		a := exp[name]
		if a == nil {
			a = &agg{maxDOP: -1}
		}
		for _, c := range []struct {
			key  string
			want int64
		}{
			{"statements", a.statements}, {"parallel_statements", a.parStatements},
			{"executions", a.execs}, {"parallel_executions", a.parExecs},
			{"parallel_executions_min", a.minExecs},
		} {
			if got := int64(pcNum(t, b[c.key], name+"."+c.key)); got != c.want {
				t.Errorf("band %s: %s = %d, want %d", name, c.key, got, c.want)
			}
		}
		for _, c := range []struct {
			key string
			us  float64
		}{{"cpu_s", a.cpu}, {"parallel_cpu_s", a.parCPU}, {"parallel_cpu_s_min", a.minCPU}} {
			if got := pcNum(t, b[c.key], name+"."+c.key); math.Abs(got-pcSeconds(c.us)) > 0.1+1e-9 {
				t.Errorf("band %s: %s = %.1f, want %.1f", name, c.key, got, pcSeconds(c.us))
			}
		}
		if a.statements == 0 {
			if b["max_dop"] != nil || b["avg_dop"] != nil {
				t.Errorf("band %s is empty and carries max_dop %v, avg_dop %v; want null", name, b["max_dop"], b["avg_dop"])
			}
			continue
		}
		if got := int64(pcNum(t, b["max_dop"], name+".max_dop")); got != a.maxDOP {
			t.Errorf("band %s: max_dop = %d, want %d", name, got, a.maxDOP)
		}
		if a.parExecs == 0 {
			if b["avg_dop"] != nil {
				t.Errorf("band %s: avg_dop = %v with no parallel execution, want null", name, b["avg_dop"])
			}
		} else if got, w := pcNum(t, b["avg_dop"], name+".avg_dop"), a.dopWeighted/float64(a.parExecs); math.Abs(got-w) > 0.01 {
			t.Errorf("band %s: avg_dop = %.2f, want %.2f", name, got, w)
		}
	}
}

// pcCheckShare checks that examined.share_of_parallel_cpu_pct is the bands'
// parallel CPU over window.parallel_cpu_s. Every figure is rounded to 0.1 s,
// the share to 0.1 %, so the share must lie inside the interval those
// roundings allow, and nowhere else.
func pcCheckShare(t *testing.T, doc map[string]any) {
	t.Helper()
	root := pcFlat(doc)
	total := pcNum(t, root["window.parallel_cpu_s"], "window.parallel_cpu_s")
	if total == 0 {
		if root["examined.share_of_parallel_cpu_pct"] != nil {
			t.Errorf("no parallel CPU in the window and a share of %v; want null", root["examined.share_of_parallel_cpu_pct"])
		}
		return
	}
	var sum float64
	var filled int
	for _, b := range pcBands(t, doc) {
		v := pcNum(t, b["parallel_cpu_s"], "parallel_cpu_s")
		sum += v
		if pcNum(t, b["statements"], "statements") > 0 {
			filled++
		}
	}
	slack := 0.05 * float64(filled)
	lo := 100*max(sum-slack, 0)/(total+0.05) - 0.05
	hi := 100*(sum+slack)/max(total-0.05, 0.05) + 0.05
	got := pcNum(t, root["examined.share_of_parallel_cpu_pct"], "examined.share_of_parallel_cpu_pct")
	if got < lo || got > hi {
		t.Errorf("share_of_parallel_cpu_pct = %.1f; the bands hold %.1f s of %.1f s, which allows %.1f to %.1f",
			got, sum, total, lo, hi)
	}
}

// pcCompareRoot checks the root's totals against the store read before the
// run and after it. The parallel totals are exact: 043's own statements are
// hinted MAXDOP 1 and reach no parallel plan. window.executions and
// window.cpu_s hold 043's own statements, which run between the two
// readings, so they must lie between the two; both leave out the queries of
// scalar functions (FN), whose CPU is in their caller and is counted in
// window.scalar_function_cpu_s. FN, TF and TR queries are nested.
func pcCompareRoot(t *testing.T, doc map[string]any, before, after []pcPlan) {
	t.Helper()
	root := pcFlat(doc)
	var plans, nested, parExecs, minExecs int64
	var planCPU, parCPU, minCPU, nestedCPU, scalarCPU float64
	for _, p := range after {
		if p.kind == "FN" {
			scalarCPU += p.cpu
		}
		if !p.parallel {
			continue
		}
		plans++
		planCPU += p.cpu
		parExecs += p.parExecs
		parCPU += p.parCPU
		minExecs += p.minExecs
		minCPU += p.minCPU
		if p.kind == "FN" || p.kind == "TF" || p.kind == "TR" {
			nested++
			nestedCPU += p.parCPU
		}
	}
	for _, c := range []struct {
		key  string
		want int64
	}{
		{"window.parallel_plans", plans}, {"window.parallel_executions", parExecs},
		{"window.parallel_executions_min", minExecs}, {"nested.parallel_plans", nested},
	} {
		if got := int64(pcNum(t, root[c.key], c.key)); got != c.want {
			t.Errorf("%s = %d, the store holds %d", c.key, got, c.want)
		}
	}
	for _, c := range []struct {
		key string
		us  float64
	}{
		{"window.parallel_plan_cpu_s", planCPU}, {"window.parallel_cpu_s", parCPU},
		{"window.parallel_cpu_s_min", minCPU}, {"nested.parallel_cpu_s", nestedCPU},
		{"window.scalar_function_cpu_s", scalarCPU},
	} {
		if got := pcNum(t, root[c.key], c.key); math.Abs(got-pcSeconds(c.us)) > 0.1+1e-9 {
			t.Errorf("%s = %.1f, the store holds %.1f", c.key, got, pcSeconds(c.us))
		}
	}
	serial := func(plans []pcPlan) (execs int64, cpu float64) {
		for _, p := range plans {
			if p.kind != "FN" {
				execs += p.execs
				cpu += p.cpu
			}
		}
		return execs, cpu
	}
	loExecs, loCPU := serial(before)
	hiExecs, hiCPU := serial(after)
	if got := int64(pcNum(t, root["window.executions"], "window.executions")); got < loExecs || got > hiExecs {
		t.Errorf("window.executions = %d, want between %d and %d, the store before and after the run, "+
			"scalar function queries left out", got, loExecs, hiExecs)
	}
	if got := pcNum(t, root["window.cpu_s"], "window.cpu_s"); got < pcSeconds(loCPU)-0.1-1e-9 || got > pcSeconds(hiCPU)+0.1+1e-9 {
		t.Errorf("window.cpu_s = %.1f, want between %.1f and %.1f, the store before and after the run, "+
			"scalar function queries left out", got, pcSeconds(loCPU), pcSeconds(hiCPU))
	}
}

// pcSettings is the instance's threshold and scheduler count, for the
// message of a fixture that came out too thin.
func pcSettings(t *testing.T, admin *sql.DB) string {
	t.Helper()
	var threshold, schedulers int
	if err := admin.QueryRow("SELECT CAST(value_in_use AS int) FROM sys.configurations " +
		"WHERE name = N'cost threshold for parallelism';").Scan(&threshold); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow("SELECT scheduler_count FROM sys.dm_os_sys_info;").Scan(&schedulers); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("cost threshold for parallelism %d, %d schedulers", threshold, schedulers)
}

// pcReplicasKnown says whether this instance can tell replica groups apart:
// the column (SQL Server 2022) and the view (measured present on 2022 CU26 and
// on 2025) both exist.
func pcReplicasKnown(t *testing.T, admin *sql.DB) (known bool, major int) {
	t.Helper()
	var col, view sql.NullInt64
	var version string
	if err := admin.QueryRow("SELECT COL_LENGTH('sys.query_store_runtime_stats', 'replica_group_id'), "+
		"OBJECT_ID('sys.query_store_replicas'), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128));").
		Scan(&col, &view, &version); err != nil {
		t.Fatal(err)
	}
	major, _ = strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	return col.Valid && view.Valid, major
}

func TestLiveQueryStoreParallelCost(t *testing.T) {
	cfg, admin := pcAdmin(t)
	// Case-sensitive, so that an identifier written in the wrong case fails
	// wherever 043 runs it, the read loop included, and named with a space
	// and a closing bracket, so that a name quoted wrongly does (Review
	// Focus 1).
	name := "ZzAvgDopLive Bands]" + pcSuffix()
	pcCreateDatabase(t, admin, name, "Latin1_General_CS_AS", false)
	pcLadder(t, cfg, name)

	// Test 3 of the spec, its assertions in its order.
	t.Run("bands", func(t *testing.T) {
		before := pcOracle(t, admin, name)
		if n := len(pcParallel(before)); n < 5 {
			t.Fatalf("the store holds %d parallel plans, want at least 5 (%s): the fixture is too thin "+
				"on this instance to tell the bands apart", n, pcSettings(t, admin))
		}
		// Plans in one band would leave every boundary but one untested: a
		// band that overlapped its neighbour would pass.
		filled := map[string]bool{}
		for _, p := range pcParallel(before) {
			if b := pcBandOf(p); b != "unknown" {
				filled[b] = true
			}
		}
		if len(filled) < 4 {
			t.Fatalf("the parallel plans fill %d of the six costed bands (%v), want at least 4 (%s): the fixture "+
				"cannot tell the boundaries apart on this instance", len(filled), filled, pcSettings(t, admin))
		}
		var scalar, nestedParallel int
		for _, p := range before {
			if p.kind == "FN" {
				scalar++
			}
			if p.parallel && (p.kind == "FN" || p.kind == "TF" || p.kind == "TR") {
				nestedParallel++
			}
		}
		if scalar == 0 || nestedParallel == 0 {
			t.Fatalf("the store holds %d scalar function queries and %d parallel nested plans, want at least one "+
				"of each (%s): the root's nested.* and its scalar function rule would test nothing",
				scalar, nestedParallel, pcSettings(t, admin))
		}
		// The premise is read from the store, and the document is then held
		// to it: read from the document alone, a 043 that excluded every row
		// would blame the fixture.
		anomalies := pcSerialAboveOne(before)
		if anomalies < 1 {
			t.Fatalf("the store holds no serial plan with max_dop above 1: the #savings anomaly did not "+
				"reproduce (%s), so a selection on max_dop could not be told from one on is_parallel_plan",
				pcSettings(t, admin))
		}
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		root := pcFlat(doc)
		if n := pcNum(t, root["window.serial_plans_dop_above_1"], "window.serial_plans_dop_above_1"); n < 1 {
			t.Errorf("window.serial_plans_dop_above_1 = %v while the store holds %d such plans", n, anomalies)
		}
		known, major := pcReplicasKnown(t, admin)
		if major >= 17 && !known {
			t.Errorf("SQL Server %d and the replica column or view was not found", major)
		}
		switch got := root["excluded.other_replicas_executions"]; {
		case known && got != float64(0):
			t.Errorf("excluded.other_replicas_executions = %v, want 0: the groups can be told apart here and the lab has no other replica", got)
		case !known && got != nil:
			t.Errorf("excluded.other_replicas_executions = %v, want null: this instance cannot tell replica groups apart", got)
		}
		plans := pcOracle(t, admin, name)
		pcCompareBands(t, doc, pcParallel(plans))
		pcCompareRoot(t, doc, before, plans)
		for _, p := range plans {
			if p.costed > 1 {
				t.Errorf("plan %d holds %d costed statement elements; the text search reads the first one only", p.id, p.costed)
			}
			// 042 calls a plan parallel when an operator carries Parallel="1";
			// 043 reads is_parallel_plan. On a plan with a cost, they must agree.
			if p.cost.Valid && p.parallel != p.marker {
				t.Errorf("plan %d: is_parallel_plan %v but an operator marked Parallel=\"1\" %v", p.id, p.parallel, p.marker)
			}
		}
		for _, b := range pcBands(t, doc) {
			if pcNum(t, b["parallel_executions_min"], "min") > pcNum(t, b["parallel_executions"], "max") {
				t.Errorf("band %v: the lower bound is above the upper one", b["band"])
			}
		}
		pcCheckShare(t, doc)
	})
	// Test 2 of the spec: nothing that names a query leaves the server.
	t.Run("no text leaves", func(t *testing.T) {
		q := quoteName(name)
		var kept int
		if err := admin.QueryRow("SELECT COUNT(*) FROM " + q + ".sys.query_store_query_text AS qt " +
			"JOIN " + q + ".sys.query_store_query AS qq ON qq.query_text_id = qt.query_text_id " +
			"JOIN " + q + ".sys.query_store_plan AS p ON p.query_id = qq.query_id " +
			"WHERE p.is_parallel_plan = 1 AND qt.query_sql_text LIKE N'%ZZ043[_]PLANTED[_]TEXT%' " +
			"OPTION (MAXDOP 1);").Scan(&kept); err != nil {
			t.Fatal(err)
		}
		if kept == 0 {
			t.Fatal("the store kept the planted literal in no parallel plan's query: a literal the store " +
				"never held cannot leak, so this would test nothing")
		}
		raw, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		var got []string
		for k := range pcFlat(doc) {
			got = append(got, k)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(pcRootKeys))
		if !slices.Equal(got, want) {
			t.Errorf("root keys\n got  %v\n want %v", got, want)
		}
		wantBand := slices.Sorted(slices.Values(pcBandKeys))
		for _, b := range pcBands(t, doc) {
			var keys []string
			for k := range b {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, wantBand) {
				t.Errorf("band %v keys\n got  %v\n want %v", b["band"], keys, wantBand)
			}
		}
		if bytes.Contains(raw, []byte("ZZ043_PLANTED_TEXT")) {
			t.Error("the planted literal is in the document")
		}
	})
	// Test 5 of the spec: the stop rules, on the same store.
	t.Run("stop", func(t *testing.T) {
		ranked := pcParallel(pcOracle(t, admin, name))
		if len(ranked) < 5 {
			t.Fatalf("the store holds %d parallel plans, want at least 5: the rules could not be told apart", len(ranked))
		}
		check := func(t *testing.T, doc map[string]any, stoppedBy any, read int, truncated float64) {
			t.Helper()
			root := pcFlat(doc)
			if got := root["examined.stopped_by"]; got != stoppedBy {
				t.Errorf("examined.stopped_by = %v, want %v", got, stoppedBy)
			}
			if got := int(pcNum(t, root["examined.plans_read"], "examined.plans_read")); got != read {
				t.Errorf("examined.plans_read = %d, want %d", got, read)
			}
			if got := pcNum(t, root["truncated"], "truncated"); got != truncated {
				t.Errorf("truncated = %v, want %v", got, truncated)
			}
		}
		t.Run("bytes after one chunk", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@chunk": 2, "@budget_bytes": 1}), name)
			check(t, doc, "bytes", 2, 1)
			first := pcParallel(pcOracle(t, admin, name))[:2]
			pcCompareBands(t, doc, first)
			want := max(first[0].bytes, first[1].bytes)
			if got := int64(pcNum(t, pcFlat(doc)["examined.largest_plan_bytes"], "examined.largest_plan_bytes")); got != want {
				t.Errorf("examined.largest_plan_bytes = %d, want %d, the larger of the two plans read", got, want)
			}
			// The share is the bands' over the window's. With two plans of
			// several read, it is below 100, so a share taken from anything
			// but the bands shows here, where a store read whole cannot.
			pcCheckShare(t, doc)
		})
		t.Run("bytes budget zero", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@budget_bytes": 0}), name)
			check(t, doc, "bytes", 0, 1)
			pcCompareBands(t, doc, nil)
		})
		t.Run("time budget zero", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@budget_ms": 0}), name)
			check(t, doc, "time", 0, 1)
			pcCompareBands(t, doc, nil)
		})
		t.Run("both budgets zero", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@budget_bytes": 0, "@budget_ms": 0}), name)
			check(t, doc, "time", 0, 1)
		})
		t.Run("cap below the plans", func(t *testing.T) {
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@cap": 2}), name)
			check(t, doc, "cap", 2, 1)
			if got := pcNum(t, pcFlat(doc)["examined.plans"], "examined.plans"); got != 2 {
				t.Errorf("examined.plans = %v, want the cap, 2", got)
			}
		})
		t.Run("cap at the plans", func(t *testing.T) {
			n := len(pcParallel(pcOracle(t, admin, name)))
			_, doc := pcRun(t, cfg, parallelCostScript(t, map[string]int64{"@cap": int64(n)}), name)
			check(t, doc, nil, n, 0)
		})
	})
	// A store in READ_ONLY is read like one in READ_WRITE (Review Focus).
	t.Run("read only store", func(t *testing.T) {
		pcExec(t, admin, "ALTER DATABASE "+quoteName(name)+" SET QUERY_STORE (OPERATION_MODE = READ_ONLY);")
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		root := pcFlat(doc)
		if root["state.actual"] != "READ_ONLY" || root["state.not_read_because"] != nil {
			t.Fatalf("state.actual %v, not_read_because %v; want READ_ONLY, read", root["state.actual"], root["state.not_read_because"])
		}
		pcCompareBands(t, doc, pcParallel(pcOracle(t, admin, name)))
	})

	// Test 6 of the spec, its second case: a store that held parallel plans,
	// then set OFF, still returns them, and must not be read. Last, since it
	// turns the store off.
	t.Run("off store", func(t *testing.T) {
		pcExec(t, admin, "ALTER DATABASE "+quoteName(name)+" SET QUERY_STORE = OFF;")
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		pcNotRead(t, doc, "OFF", "off")
	})
}

// pcNotReadKeys are the root keys that are NULL when a store is not read:
// everything but the identity, the state, the constants and the window's
// definition.
func pcNotReadKeys() []string {
	keep := map[string]bool{
		"database": true, "collected_at": true, "schedulers": true,
		"state.actual": true, "state.capture_mode": true, "state.interval_minutes": true,
		"state.not_read_because": true, "window.days": true, "window.from": true,
		"cap": true, "chunk": true, "budget.bytes": true, "budget.ms": true,
	}
	var out []string
	for _, k := range pcRootKeys {
		if !keep[k] {
			out = append(out, k)
		}
	}
	return out
}

// pcNotRead checks a document of a store that was not read: the state and
// the reason, every count NULL, and seven empty bands.
func pcNotRead(t *testing.T, doc map[string]any, actual any, reason string) {
	t.Helper()
	root := pcFlat(doc)
	if root["state.actual"] != actual || root["state.not_read_because"] != reason {
		t.Errorf("state.actual %v, not_read_because %v; want %v, %s", root["state.actual"], root["state.not_read_because"], actual, reason)
	}
	for _, k := range pcNotReadKeys() {
		if root[k] != nil {
			t.Errorf("%s = %v on a store that was not read, want null", k, root[k])
		}
	}
	pcCompareBands(t, doc, nil)
}

// Test 6 of the spec, its first and third cases.
func TestLiveQueryStoreParallelCostStoresNotRead(t *testing.T) {
	cfg, admin := pcAdmin(t)

	// master has no row in the options view: the "never enabled" case,
	// which a new database cannot be on SQL Server 2022 and later. Measured
	// with no row on 2017 CU31, 2022 CU26 and 2025 CU7, the CI legs and the
	// lab.
	t.Run("master", func(t *testing.T) {
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), "master")
		pcNotRead(t, doc, nil, "no store")
	})

	// A store that is on and holds serial work only. Case-sensitive, so that
	// an identifier written in the wrong case fails here, and named with a
	// space and a closing bracket, so that a name quoted wrongly does
	// (Review Focus). Automatic statistics are off: a StatMan query can be
	// stored as a parallel plan, and this store must hold none.
	t.Run("serial only", func(t *testing.T) {
		name := "ZzAvgDopLive Serial]" + pcSuffix()
		pcCreateDatabase(t, admin, name, "Latin1_General_CS_AS", true)
		w := pcOpen(t, cfg, name)
		pcExec(t, w, "CREATE TABLE dbo.Serial1 (id bigint NOT NULL, pad char(100) NOT NULL);")
		pcExec(t, w, "INSERT INTO dbo.Serial1 WITH (TABLOCK) (id, pad) "+
			"SELECT TOP (500000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x' "+
			"FROM sys.all_columns AS a CROSS JOIN sys.all_columns AS b CROSS JOIN sys.all_columns AS c "+
			"OPTION (MAXDOP 1);")
		for range 5 {
			pcExec(t, w, "SELECT COUNT_BIG(*) FROM dbo.Serial1 WHERE id % 7 = 3 OPTION (MAXDOP 1);")
		}
		pcExec(t, w, "EXEC sys.sp_query_store_flush_db;")
		if n := len(pcParallel(pcOracle(t, admin, name))); n != 0 {
			t.Fatalf("the serial store holds %d parallel plans; the case needs none", n)
		}
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), name)
		root := pcFlat(doc)
		if root["state.actual"] != "READ_WRITE" || root["state.not_read_because"] != nil {
			t.Fatalf("state.actual %v, not_read_because %v; want READ_WRITE, read", root["state.actual"], root["state.not_read_because"])
		}
		for _, k := range []string{
			"window.parallel_plans", "window.parallel_plan_cpu_s", "window.parallel_executions",
			"window.parallel_cpu_s", "window.parallel_executions_min", "window.parallel_cpu_s_min",
			"nested.parallel_plans", "nested.parallel_cpu_s",
			"examined.plans", "examined.plans_read", "examined.bytes_read",
		} {
			if root[k] != float64(0) {
				t.Errorf("%s = %v, want 0 and not null: the store was read and holds no parallel plan", k, root[k])
			}
		}
		if root["examined.share_of_parallel_cpu_pct"] != nil {
			t.Errorf("examined.share_of_parallel_cpu_pct = %v, want null with no parallel CPU", root["examined.share_of_parallel_cpu_pct"])
		}
		for _, k := range []string{"window.executions", "window.cpu_s"} {
			if pcNum(t, root[k], k) <= 0 {
				t.Errorf("%s = %v, want above 0: the serial work and 043's own statements are in it", k, root[k])
			}
		}
		pcCompareBands(t, doc, nil)
	})

	// Owner's ruling of 6 October 2026: a database restored WITH STANDBY
	// (log shipping) holds the source server's store and is not read, as an
	// availability group secondary is not. The backup and the undo file sit in
	// the instance's data directory (the default of the container) and are
	// removed at the end, with both databases.
	t.Run("standby", func(t *testing.T) {
		suffix := pcSuffix()
		src := "ZzAvgDopLiveStandbySrc" + suffix
		dst := "ZzAvgDopLiveStandby" + suffix
		var dir string
		if err := admin.QueryRow("SELECT CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(260));").Scan(&dir); err != nil || dir == "" {
			t.Fatalf("the instance's data path: %q, %v", dir, err)
		}
		bak := dir + "zzavgdop-" + suffix + ".bak"
		undo := dir + "zzavgdop-" + suffix + ".undo"
		mdf := dir + "zzavgdop-" + suffix + "-sb.mdf"
		ldf := dir + "zzavgdop-" + suffix + "-sb.ldf"
		// Registered first, so it runs last, after both drops.
		t.Cleanup(func() {
			out, err := exec.Command("podman", "exec", "sql2025", "rm", "-f", bak, undo, mdf, ldf).CombinedOutput()
			if err != nil {
				t.Logf("removing %s, %s, %s, %s: %v %s; remove them by hand if the server is a container of yours", bak, undo, mdf, ldf, err, out)
			}
		})
		pcCreateDatabase(t, admin, src, "", false)
		w := pcOpen(t, cfg, src)
		pcExec(t, w, "CREATE TABLE dbo.Standby1 (id bigint NOT NULL, pad char(100) NOT NULL);")
		pcExec(t, w, "INSERT INTO dbo.Standby1 (id, pad) SELECT TOP (1000) ROW_NUMBER() OVER (ORDER BY (SELECT NULL)), 'x' "+
			"FROM sys.all_columns OPTION (MAXDOP 1);")
		pcExec(t, w, "SELECT COUNT_BIG(*) FROM dbo.Standby1 WHERE id % 7 = 3 OPTION (MAXDOP 1);")
		pcExec(t, w, "EXEC sys.sp_query_store_flush_db;")
		pcExec(t, admin, "BACKUP DATABASE "+quoteName(src)+" TO DISK = N'"+bak+"' WITH INIT, FORMAT;")
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "IF DB_ID(@p1) IS NOT NULL DROP DATABASE "+quoteName(dst)+";", dst); err != nil {
				t.Errorf("dropping %s: %v; drop it by hand", dst, err)
			}
		})
		pcExec(t, admin, "RESTORE DATABASE "+quoteName(dst)+" FROM DISK = N'"+bak+"' WITH "+
			"MOVE N'"+src+"' TO N'"+mdf+"', MOVE N'"+src+"_log' TO N'"+ldf+"', STANDBY = N'"+undo+"';")
		var inStandby bool
		if err := admin.QueryRow("SELECT is_in_standby FROM sys.databases WHERE name = @p1;", dst).Scan(&inStandby); err != nil || !inStandby {
			t.Fatalf("the restored database is not in standby: %v, %v", inStandby, err)
		}
		_, doc := pcRun(t, cfg, parallelCostScript(t, nil), dst)
		pcNotRead(t, doc, "READ_ONLY", "standby")
	})
}
