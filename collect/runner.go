package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

// badAddress marks a failure to make sense of SQL_SERVER, as opposed to a
// failure to reach the instance it names. parseServer refuses an empty
// address, a trailing backslash and an unbracketed IPv6 literal before any
// socket is opened, so the operator has a typo, not an unreachable server.
//
// The distinction is the exit code. 1 is documented as "the instance could not
// be reached" and sends a DBA to check a server that was never contacted; a
// configuration mistake is 2, as it already is for an unreadable
// --queries-dir. IsBadServerAddress is how Run and Check tell the two apart.
type badAddress struct{ err error }

func (b badAddress) Error() string { return b.err.Error() }
func (b badAddress) Unwrap() error { return b.err }

// IsBadServerAddress reports whether err comes from parsing the configured
// server address rather than from contacting the instance.
func IsBadServerAddress(err error) bool {
	var b badAddress
	return errors.As(err, &b)
}

// fail is parseServer's only error path, so no refusal can be added later that
// forgets the marker and silently reports a typo as an unreachable instance.
func fail(format string, a ...any) (string, string, error) {
	return "", "", badAddress{fmt.Errorf(format, a...)}
}

type ServerInfo struct {
	Name, Version, Edition string
	// Login is the connected principal as the SERVER names it. It is not the
	// login string from the configuration: a Windows login arrives as
	// DOMAIN\user whatever case was typed, and a login can be reached through
	// a group. Anything that writes the login into a GRANT has to use this one
	// or it grants permissions to a principal nobody is connecting with.
	Login            string
	UTCOffsetMinutes int
	// NowUTC is the instant the SERVER reports, not the one the collecting
	// machine believes. The two differ by more than a zone: a server whose
	// clock runs a few minutes ahead of the auditor's laptop would make a
	// Query Store bound typed as "just now" look like a bound in the future.
	// Anything that compares a bound against "now" has to use this one, or the
	// check and the message it prints refer to two different clocks.
	//
	// Zero when the server did not answer with one, which callers treat as
	// "fall back to the local instant" rather than as the year 1.
	NowUTC time.Time
}

type DatabaseInfo struct {
	Name, State string
	IsSnapshot  bool
	HasAccess   bool
	// The three replication roles, read from sys.databases in the same pass
	// that lists the candidates. They are flags and not proof of activity: a
	// database restored from a publisher keeps them set, which is why the
	// collectors that use them record what they found rather than trusting the
	// flag.
	IsPublished   bool
	IsSubscribed  bool
	IsDistributor bool
}

// SkipNoAccess is the reason a database is left out because the login
// cannot connect to it. It is a constant because two places must agree on
// it: the run that records the skip, and the grant script that offers to
// fix it. A literal in both would drift and the fix would silently stop
// being offered.
const SkipNoAccess = "no access for this login"

// skipNotIncluded is the reason DB_INCLUDE did not name a database. It is a
// constant for the same reason as the one above, and here the two places are
// the first pass that records the skip and the second pass that supersedes it
// when a distributor is kept. A literal in both would drift, the supersession
// would quietly stop matching, and the manifest would then name one database
// twice with two contradictory reasons.
const skipNotIncluded = "not matched by DB_INCLUDE"

type SkipReason struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type Selection struct {
	Included []string
	Skipped  []SkipReason
	// Widened maps a database name to why the second pass kept it despite
	// DB_INCLUDE. Empty for every ordinary run: with no DB_INCLUDE every user
	// database is already included and the pass changes nothing.
	Widened map[string]WidenedFor
}

// WidenedFor is why a database the operator did not name is in the run. The
// two halves are separate on purpose and must stay that way.
//
// Purpose is a machine token from the same closed vocabulary as the @widened
// directive, and planUnits matches it against a script's own value. Reason is
// a sentence for whoever reads MANIFEST.txt.
//
// They were one field for about an hour. Because planUnits compares the field
// to "replication" and the field held "local distributor for 1 published
// database(s) in this selection", every comparison failed and the widened
// database was offered to no collector at all — including the ones it had been
// kept for. Nothing caught it: each unit test built its own fixture with its
// own idea of what the field contained.
type WidenedFor struct {
	Purpose string
	Reason  string
}

// parseServer normalises the ways a SQL Server address is written into the two
// pieces the sqlserver:// URL needs: an authority for u.Host and an instance
// name for u.Path.
//
// The driver's URL parser accepts only host:port. Every other canonical form
// fails, and the named-instance one fails before a socket is opened:
//
//	localhost,11433       lookup localhost,11433: no such host
//	HOST\SQLEXPRESS       unable to parse connection string: invalid URL format
//
// host,port is what .env.example documents and what every PowerShell-era
// configuration contains, and HOST\INSTANCE is how SQL Express and every
// multi-instance box is addressed, so both have to be translated here.
//
// Accepted: host, host,port, host:port, host\instance, host\instance,port,
// each optionally prefixed tcp:, and IPv6 literals in brackets.
func parseServer(server string) (hostport, instance string, err error) {
	s := strings.TrimSpace(server)
	if s == "" {
		return fail("server address is empty")
	}
	// A tcp: prefix names the network protocol and is not part of the address.
	// Guard against an IPv6 literal, which also starts with a colon-heavy run.
	if len(s) > 4 && strings.EqualFold(s[:4], "tcp:") {
		s = strings.TrimSpace(s[4:])
	}
	// Instance first: the backslash always precedes the port, so splitting here
	// leaves at most one ",port" or ":port" behind.
	if i := strings.IndexByte(s, '\\'); i >= 0 {
		s, instance = s[:i], strings.TrimSpace(s[i+1:])
		if instance == "" {
			return fail("server address %q has a backslash but no instance name", server)
		}
		if j := strings.IndexAny(instance, ",:"); j >= 0 {
			instance, s = instance[:j], s+instance[j:]
		}
	}
	host, port := s, ""
	// An IPv6 literal is bracketed and full of colons, so the port separator is
	// only whatever follows the closing bracket.
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return fail("server address %q has an unclosed IPv6 bracket", server)
		}
		rest := host[end+1:]
		host = host[:end+1]
		switch {
		case rest == "":
		case rest[0] == ',' || rest[0] == ':':
			port = strings.TrimSpace(rest[1:])
		default:
			return fail("server address %q: unexpected %q after the IPv6 literal", server, rest)
		}
	} else if i := strings.IndexByte(host, ','); i >= 0 {
		host, port = host[:i], strings.TrimSpace(host[i+1:])
	} else if i := strings.LastIndexByte(host, ':'); i >= 0 {
		// Only a bare host:port here — an unbracketed multi-colon string is an
		// IPv6 literal missing its brackets, not a port.
		if strings.IndexByte(host, ':') != i {
			return fail("server address %q looks like an IPv6 literal; wrap it in brackets, as [::1],1433", server)
		}
		host, port = host[:i], strings.TrimSpace(host[i+1:])
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fail("server address %q has no host", server)
	}
	// A colon still left in an unbracketed host means an IPv6 literal written
	// without brackets, which url.Parse would silently mangle.
	if !strings.HasPrefix(host, "[") && strings.Contains(host, ":") {
		return fail("server address %q looks like an IPv6 literal; wrap it in brackets, as [::1],1433", server)
	}
	if port == "" {
		return host, instance, nil
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fail("server address %q: %q is not a port number", server, port)
	}
	return host + ":" + port, instance, nil
}

// connURL builds the sqlserver:// URL Open connects with. It is separate from
// Open so the connection string can be asserted on without a server.
func connURL(cfg *Config) (*url.URL, error) {
	hostport, instance, err := parseServer(cfg.Server)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("database", cfg.Database)
	q.Set("app name", cfg.AppName)
	// "dial timeout", never "connection timeout". The driver hands the latter
	// to newTimeoutConn, which re-arms a socket deadline before every read for
	// the whole life of the session — so it does not bound dialling, it caps
	// every query at ConnectTimeout and the @timeout a collector declares
	// never gets to apply. Observed against a real instance: collectors
	// declaring @timeout 300 died at 15 s with "Invalid TDS stream: ...
	// i/o timeout" on exactly the databases whose work exceeded 15 s. The
	// driver's own source says not to set it ("Do not set a connection
	// timeout. Use Context to manage such things.", msdsn/conn_str.go), and
	// every call site here already carries a context deadline.
	q.Set("dial timeout", fmt.Sprint(int(cfg.ConnectTimeout.Seconds())))
	q.Set("encrypt", fmt.Sprint(cfg.Encrypt))
	q.Set("TrustServerCertificate", fmt.Sprint(cfg.TrustCert))
	u := &url.URL{Scheme: "sqlserver", Host: hostport, RawQuery: q.Encode()}
	if instance != "" {
		u.Path = "/" + instance
	}
	if cfg.User != "" && !cfg.Integrated {
		u.User = url.UserPassword(cfg.User, cfg.Password)
	}
	return u, nil
}

// Open builds a sqlserver:// URL from cfg and returns a pool pinned to a single
// connection. It does not contact the server: database/sql connects lazily, so
// a bad address surfaces on the first query, not here.
func Open(cfg *Config) (*sql.DB, error) {
	u, err := connURL(cfg)
	if err != nil {
		return nil, err
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

// Probe reads the server's identity and clock offset. It takes a *sql.Conn,
// not a *sql.DB: the pool allows exactly one connection, so a caller holding a
// Conn that also passed the DB here would deadlock until its context expired.
//
// Every column is scanned nullably and a NULL costs only its own field, never
// the whole probe. SERVERPROPERTY('ServerName') is NULL on some Azure and
// contained configurations, and scanning that into a *string fails the entire
// row with "converting NULL to string is unsupported".
func Probe(ctx context.Context, c *sql.Conn) (ServerInfo, error) {
	var si ServerInfo
	var name, version, edition, login sql.NullString
	var offset sql.NullInt64
	var nowUTC sql.NullTime
	err := c.QueryRowContext(ctx, `
        SELECT CONVERT(nvarchar(128), SERVERPROPERTY('ServerName')),
               CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion')),
               CONVERT(nvarchar(128), SERVERPROPERTY('Edition')),
               DATEDIFF(MINUTE, GETUTCDATE(), GETDATE()),
               SUSER_SNAME(),
               GETUTCDATE()`).
		Scan(&name, &version, &edition, &offset, &login, &nowUTC)
	if err != nil {
		return si, err
	}
	si.Name, si.Version, si.Edition, si.Login = name.String, version.String, edition.String, login.String
	si.UTCOffsetMinutes = int(offset.Int64)
	if nowUTC.Valid {
		si.NowUTC = nowUTC.Time.UTC()
	}
	return si, nil
}

// CandidateDatabases lists every user database with the facts SelectTargets
// needs. It takes a *sql.Conn for the same reason Probe does.
func CandidateDatabases(ctx context.Context, c *sql.Conn) ([]DatabaseInfo, error) {
	rows, err := c.QueryContext(ctx, `
        SELECT d.name, d.state_desc,
               CASE WHEN d.source_database_id IS NULL THEN 0 ELSE 1 END,
               CASE WHEN HAS_DBACCESS(d.name) = 1 THEN 1 ELSE 0 END,
               CONVERT(int, d.is_published),
               CONVERT(int, d.is_subscribed),
               CONVERT(int, d.is_distributor)
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
		var snap, acc, pub, sub, dist int
		if err := rows.Scan(&d.Name, &d.State, &snap, &acc, &pub, &sub, &dist); err != nil {
			return nil, err
		}
		d.IsSnapshot, d.HasAccess = snap == 1, acc == 1
		d.IsPublished, d.IsSubscribed, d.IsDistributor = pub == 1, sub == 1, dist == 1
		out = append(out, d)
	}
	return out, rows.Err()
}

// SelectTargets applies include/exclude wildcards and records a reason for
// every exclusion, so the manifest can explain an absent database rather than
// leaving the analysis layer to guess.
//
// A malformed pattern is an error, not a pattern that matches nothing: an
// unclosed [ in DB_EXCLUDE would otherwise collect a database the user
// explicitly asked to leave alone, and say nothing about it.
func SelectTargets(c []DatabaseInfo, include, exclude string) (Selection, error) {
	var sel Selection
	inc, exc := splitPatterns(include), splitPatterns(exclude)
	if err := checkPatterns("DB_INCLUDE", inc); err != nil {
		return sel, err
	}
	if err := checkPatterns("DB_EXCLUDE", exc); err != nil {
		return sel, err
	}
	for _, d := range c {
		switch {
		case d.State != "ONLINE":
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "state=" + d.State})
		case d.IsSnapshot:
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "database snapshot"})
		case !d.HasAccess:
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, SkipNoAccess})
		case len(inc) > 0 && !matchAny(inc, d.Name):
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, skipNotIncluded})
		case matchAny(exc, d.Name):
			sel.Skipped = append(sel.Skipped, SkipReason{d.Name, "matched by DB_EXCLUDE"})
		default:
			sel.Included = append(sel.Included, d.Name)
		}
	}

	// Second pass: keep the distribution database when a publisher survived
	// the first one.
	//
	// It fires only where it is needed. With no DB_INCLUDE every user database
	// is already included and this changes nothing. It exists for the narrowed
	// run whose narrowed set still contains a publisher, where losing the
	// distributor that goes with it would be absurd — the log that will not
	// truncate is on the publisher and the reason is in the distributor.
	//
	// "Retained after filtering" is the exact trigger, and sel.Included is
	// what encodes it: a publisher the operator excluded, or one the login
	// cannot reach, is not in there and does not count. Re-testing DB_INCLUDE
	// here would be a tautology.
	published := 0
	for _, d := range c {
		if d.IsPublished && containsString(sel.Included, d.Name) {
			published++
		}
	}
	if published == 0 {
		return sel, nil
	}
	for _, d := range c {
		if !d.IsDistributor || containsString(sel.Included, d.Name) {
			continue
		}
		// Everything except DB_INCLUDE still disqualifies it. DB_EXCLUDE in
		// particular stays the operator's explicit way of saying no.
		if d.State != "ONLINE" || d.IsSnapshot || !d.HasAccess || matchAny(exc, d.Name) {
			continue
		}
		// The skip this supersedes has to go, or the manifest names the same
		// database twice with two contradictory reasons. Only that one reason
		// can be superseded: the others are still true.
		for i, s := range sel.Skipped {
			if s.Name == d.Name && s.Reason == skipNotIncluded {
				sel.Skipped = append(sel.Skipped[:i], sel.Skipped[i+1:]...)
				break
			}
		}
		sel.Included = append(sel.Included, d.Name)
		if sel.Widened == nil {
			sel.Widened = map[string]WidenedFor{}
		}
		sel.Widened[d.Name] = WidenedFor{
			Purpose: "replication",
			Reason: fmt.Sprintf(
				"local distributor for %d published database(s) in this selection",
				published),
		}
	}
	return sel, nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// checkPatterns validates each pattern once, up front, so a syntax error is
// reported against the setting the user wrote rather than silently changing
// which databases are collected.
func checkPatterns(setting string, patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(strings.ToLower(p), ""); err != nil {
			return fmt.Errorf("%s: invalid pattern %q: %w", setting, p, err)
		}
	}
	return nil
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
//
// The caller owns rows and must close it. The pool allows one connection, so a
// caller that forgets does not leak a connection, it hangs the entire run at
// the next query.
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
