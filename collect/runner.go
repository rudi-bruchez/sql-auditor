package collect

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path"
	"strings"

	_ "github.com/microsoft/go-mssqldb"
)

type ServerInfo struct {
	Name, Version, Edition string
	UTCOffsetMinutes       int
}

type DatabaseInfo struct {
	Name, State string
	IsSnapshot  bool
	HasAccess   bool
}

type SkipReason struct{ Name, Reason string }

type Selection struct {
	Included []string
	Skipped  []SkipReason
}

func Open(cfg *Config) (*sql.DB, error) {
	q := url.Values{}
	q.Set("database", cfg.Database)
	q.Set("app name", cfg.AppName)
	q.Set("connection timeout", fmt.Sprint(int(cfg.ConnectTimeout.Seconds())))
	q.Set("encrypt", fmt.Sprint(cfg.Encrypt))
	q.Set("TrustServerCertificate", fmt.Sprint(cfg.TrustCert))
	u := &url.URL{Scheme: "sqlserver", Host: cfg.Server, RawQuery: q.Encode()}
	if cfg.User != "" && !cfg.Integrated {
		u.User = url.UserPassword(cfg.User, cfg.Password)
	}
	db, err := sql.Open("sqlserver", u.String())
	if err != nil {
		return nil, err
	}
	// One connection for the whole run: session state (database context,
	// SET options) must be predictable between scripts.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func Probe(ctx context.Context, db *sql.DB) (ServerInfo, error) {
	var si ServerInfo
	// Columns are read individually: a single NULL in a multi-column SELECT
	// of SERVERPROPERTY would propagate and lose the others.
	err := db.QueryRowContext(ctx, `
        SELECT CONVERT(nvarchar(128), SERVERPROPERTY('ServerName')),
               CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion')),
               CONVERT(nvarchar(128), SERVERPROPERTY('Edition')),
               DATEDIFF(MINUTE, GETUTCDATE(), GETDATE())`).
		Scan(&si.Name, &si.Version, &si.Edition, &si.UTCOffsetMinutes)
	return si, err
}

func CandidateDatabases(ctx context.Context, db *sql.DB) ([]DatabaseInfo, error) {
	rows, err := db.QueryContext(ctx, `
        SELECT d.name, d.state_desc,
               CASE WHEN d.source_database_id IS NULL THEN 0 ELSE 1 END,
               CASE WHEN HAS_DBACCESS(d.name) = 1 THEN 1 ELSE 0 END
        FROM sys.databases AS d
        WHERE d.database_id > 4
        ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DatabaseInfo
	for rows.Next() {
		var d DatabaseInfo
		var snap, acc int
		if err := rows.Scan(&d.Name, &d.State, &snap, &acc); err != nil {
			return nil, err
		}
		d.IsSnapshot, d.HasAccess = snap == 1, acc == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

// SelectTargets applies include/exclude wildcards and records a reason for
// every exclusion, so the manifest can explain an absent database rather than
// leaving the analysis layer to guess.
func SelectTargets(c []DatabaseInfo, include, exclude string) Selection {
	var sel Selection
	inc, exc := splitPatterns(include), splitPatterns(exclude)
	for _, d := range c {
		switch {
		case d.State != "ONLINE":
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "state=" + d.State})
		case d.IsSnapshot:
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "database snapshot"})
		case !d.HasAccess:
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "no access for this login"})
		case len(inc) > 0 && !matchAny(inc, d.Name):
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "not matched by DB_INCLUDE"})
		case matchAny(exc, d.Name):
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "matched by DB_EXCLUDE"})
		default:
			sel.Included = append(sel.Included, d.Name)
		}
	}
	return sel
}

func splitPatterns(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(name)); ok {
			return true
		}
	}
	return false
}

// ResetSession rolls back a transaction leaked by the previous unit and
// returns the connection to the default database. It runs before every unit
// AND after every reconnect — the PowerShell version skipped the reconnect
// case, quietly breaking its own invariant.
func ResetSession(ctx context.Context, c *sql.Conn, defaultDB string) error {
	_, err := c.ExecContext(ctx, fmt.Sprintf(
		"IF @@TRANCOUNT > 0 ROLLBACK; USE %s;", quoteName(defaultDB)))
	return err
}

func quoteName(n string) string {
	return "[" + strings.ReplaceAll(n, "]", "]]") + "]"
}

// ReadResultSets materialises every result set in order, matching each to its
// declared spec.
func ReadResultSets(rows *sql.Rows, specs []ResultSpec) ([]NamedResultSet, error) {
	var out []NamedResultSet
	for i := 0; ; i++ {
		if i >= len(specs) {
			return nil, fmt.Errorf("query returned more result sets than the %d declared in @resultsets", len(specs))
		}
		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		// The SQL type name per column is required by the encoder: DATETIME
		// and DATETIMEOFFSET both arrive as time.Time, and DECIMAL,
		// VARBINARY and UNIQUEIDENTIFIER all arrive as []byte. Without this,
		// a decimal is emitted as a hex string.
		colTypes, err := rows.ColumnTypes()
		if err != nil {
			return nil, err
		}
		types := make([]string, len(colTypes))
		for j, ct := range colTypes {
			types[j] = strings.ToUpper(ct.DatabaseTypeName())
		}
		set := ResultSet{Columns: cols, Types: types}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for j := range vals {
				ptrs[j] = &vals[j]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			set.Rows = append(set.Rows, vals)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out = append(out, NamedResultSet{Spec: specs[i], Set: set})
		if !rows.NextResultSet() {
			break
		}
	}
	if len(out) != len(specs) {
		return nil, fmt.Errorf("query returned %d result sets but @resultsets declares %d", len(out), len(specs))
	}
	return out, nil
}
