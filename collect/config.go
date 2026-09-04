// Package collect implements the sql-auditor collection pipeline.
package collect

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WriteEnvTemplate writes the annotated configuration template to dest, which
// is what `sql-auditor env init` does. The content is passed in rather than
// read here, because the template is embedded in the root package and this one
// must not import it.
//
// It is written verbatim. The comments are the greater part of its value — the
// key set is closed, so the file is the only place that says what may appear in
// it — and rendering the keys back out of a parsed form would drop exactly that.
//
// An existing file stops the command unless force is set. The target is where
// the operator's server and, frequently, their password live; a command whose
// purpose is to produce a starting point must not be the one that destroys the
// finished configuration. O_EXCL does the refusing, so the check and the write
// are one operation and a second process cannot slip between them.
func WriteEnvTemplate(content, dest string, force bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	// 0600 rather than 0644: the file this creates is the one meant to hold
	// SQL_PASSWORD, and it is easier to widen a file than to notice it was
	// world-readable for the fortnight before anyone looked.
	f, err := os.OpenFile(dest, flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; move it aside, or pass --force to replace it", dest)
		}
		return err
	}
	defer f.Close()
	// The perm argument above applies only when the file is created, so a
	// --force over an existing 0644 .env would have kept it world-readable —
	// which is the opposite of what the mode is there for, and least visible
	// on exactly the file that goes on to hold SQL_PASSWORD.
	if err := f.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	if _, err := io.WriteString(f, content); err != nil {
		return err
	}
	return f.Close()
}

type Config struct {
	Server, Database, User, Password, AppName string
	// AppNameSet distinguishes an AppName the operator supplied from the one
	// this package defaulted to. The two cannot be told apart from the value:
	// SQL_APPLICATION_NAME=sql-auditor is a legitimate thing to write — it is
	// the worked example in .env.example — and the caller must not decorate it.
	AppNameSet                     bool
	Integrated, Encrypt, TrustCert bool
	ConnectTimeout, QueryTimeout   time.Duration
	QueriesDir, OutputDir          string
	DBInclude, DBExclude           string
	QueryStoreDays                 int
	QueryStoreFrom, QueryStoreTo   string
	QueryStoreTop                  int
	QueryStoreDBInclude            string
	// QueryStoreWindowConflict is set when QUERY_STORE_DAYS was typed
	// alongside QUERY_STORE_FROM or QUERY_STORE_TO. Resolve is the only place
	// that can see the conflict — downstream only ever sees a resolved int, so
	// "typed" and "defaulted" are no longer distinguishable — but it is not the
	// place that can price it: whether an unusable window is fatal depends on
	// the Query Store flags, which Resolve never sees. So the fact travels and
	// resolveWindow reports it, where windowForRun already decides what an
	// unusable window costs.
	QueryStoreWindowConflict string
}

// maxQueryStoreDays caps QUERY_STORE_DAYS at ten years. The Query Store's own
// retention is configured in days (stale_query_threshold_days) and is measured
// in weeks or months on real instances, so nothing above this is a window
// anybody meant to ask for.
//
// The ceiling is not tidiness. The sliding window multiplies the value by 24
// hours into a time.Duration, and a large enough count overflows int64 into a
// NEGATIVE duration: the start of the window then lands after its end, the
// extraction matches nothing, and the run reads as a clean collection of a
// quiet instance — exactly what the window refusals exist to prevent.
const maxQueryStoreDays = 3650

// knownKeys is the closed set of recognised settings. Anything else in a
// .env file is a typo, and a typo that silently changes behaviour (SQL_LOGIN
// falling through to integrated auth) is worse than a hard failure.
var knownKeys = map[string]bool{
	"SQL_SERVER": true, "SQL_DATABASE": true, "SQL_USER": true,
	"SQL_PASSWORD": true, "SQL_INTEGRATED_SECURITY": true,
	"SQL_ENCRYPT": true, "SQL_TRUST_SERVER_CERTIFICATE": true,
	"SQL_CONNECT_TIMEOUT_SEC": true, "SQL_QUERY_TIMEOUT_SEC": true,
	"SQL_APPLICATION_NAME": true, "QUERIES_DIR": true, "OUTPUT_DIR": true,
	"DB_INCLUDE": true, "DB_EXCLUDE": true,
	"QUERY_STORE_DAYS": true, "QUERY_STORE_FROM": true, "QUERY_STORE_TO": true,
	"QUERY_STORE_TOP": true, "QUERY_STORE_DB_INCLUDE": true,
}

// DefaultAppName is what SQL Server reports in program_name when the operator
// set no SQL_APPLICATION_NAME. It is exported because the caller stamps the
// version onto it: the version lives in main, where the release workflow can
// reach it with -ldflags, and this package must not learn about it just to
// build a string.
const DefaultAppName = "sql-auditor"

// renamed maps retired key names to their replacement, so the error can say
// what to do instead of only what is wrong.
var renamed = map[string]string{"SQL_LOGIN": "SQL_USER"}

// ParseDotEnv reads KEY=VALUE lines. It honours a leading "export ", single
// and double quotes, and strips unquoted trailing comments. Multi-line values
// are not supported.
func ParseDotEnv(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", line, s)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch {
		case len(v) >= 1 && (v[0] == '"' || v[0] == '\''):
			// Quoted value: take everything up to the matching close quote
			// and discard anything after it (including a trailing comment),
			// so a "#" inside the quotes is preserved verbatim.
			q := v[0]
			if end := strings.IndexByte(v[1:], q); end >= 0 {
				v = v[1 : end+1]
			}
		default:
			if i := strings.Index(v, " #"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
		}
		out[k] = v
	}
	return out, sc.Err()
}

// Resolve applies precedence: flag, then .env, then process environment, then
// default. The .env-over-environment ordering is deliberate and documented;
// it matches the PowerShell extractor that existing configurations were
// written for.
func Resolve(flags, dotenv map[string]string, environ func(string) string) (*Config, error) {
	if err := checkKeys(dotenv); err != nil {
		return nil, err
	}
	get := func(key, def string) string {
		if v, ok := flags[key]; ok && v != "" {
			return v
		}
		if v, ok := dotenv[key]; ok && v != "" {
			return v
		}
		if v := environ(key); v != "" {
			return v
		}
		return def
	}
	// set reports that the operator supplied the key, wherever from. It is not
	// the same question as "does the value differ from the default", and one
	// key needs the difference: SQL_APPLICATION_NAME=sql-auditor written out in
	// full is a choice, and .env.example ships exactly that line as its worked
	// example. Comparing against the default would treat it as absent and
	// append a version to it, breaking the Extended Events filter it was
	// written to be matched by.
	set := func(key string) bool { return get(key, "") != "" }
	// firstErr captures the first malformed-value error encountered by
	// boolOf or secOf. An absent or empty value still takes the default
	// silently; only a present-but-unparseable value is an error — that
	// matches the hard-fail stance already taken on unknown keys and on
	// SQL_LOGIN.
	var firstErr error
	boolOf := func(key string, def bool) bool {
		raw := get(key, "")
		if raw == "" {
			return def
		}
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: invalid value %q, want one of 1/true/yes/on or 0/false/no/off", key, raw)
		}
		return def
	}
	secOf := func(key string, def int) time.Duration {
		raw := get(key, "")
		if raw == "" {
			return time.Duration(def) * time.Second
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: invalid value %q, want a positive whole number of seconds", key, raw)
			}
			return time.Duration(def) * time.Second
		}
		return time.Duration(n) * time.Second
	}
	// intOf mirrors secOf but for plain positive counts (no seconds unit),
	// used by QUERY_STORE_DAYS and QUERY_STORE_TOP. max is the largest value
	// accepted, or 0 for no ceiling: a count that is only ever compared stays
	// unbounded, a count that becomes a duration must not be.
	intOf := func(key string, def, max int) int {
		raw := get(key, "")
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil || n <= 0:
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: invalid value %q, want a positive whole number", key, raw)
			}
			return def
		case max > 0 && n > max:
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: invalid value %q, want a whole number between 1 and %d", key, raw, max)
			}
			return def
		}
		return n
	}
	// dateShapeOf checks that, when present, a QUERY_STORE_FROM/TO value has
	// the shape "2006-01-02T15:04" or "2006-01-02". It is deliberately not
	// resolved to an instant here: Resolve does not know the server's UTC
	// offset, and doing so would silently bake in the collecting machine's
	// zone instead. resolveWindow turns it into an instant once the probe has
	// reported the offset.
	dateShapeOf := func(key string) string {
		raw := get(key, "")
		if raw == "" {
			return ""
		}
		if _, err := time.Parse("2006-01-02T15:04", raw); err == nil {
			return raw
		}
		if _, err := time.Parse("2006-01-02", raw); err == nil {
			return raw
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: invalid value %q, want 2006-01-02T15:04 or 2006-01-02", key, raw)
		}
		return raw
	}

	// QUERY_STORE_DAYS interacts with QUERY_STORE_FROM/TO, and Resolve is the
	// only place that can see whether QUERY_STORE_DAYS was typed or defaulted
	// (downstream only ever sees a resolved int). The conflict is therefore
	// DETECTED here, and before the default of 7 is applied — once the default
	// is in, a legitimate "--query-store-from" run is indistinguishable from one
	// that also asked for seven days.
	//
	// It is recorded rather than refused. Resolve runs before any flag is
	// consulted, so refusing here killed a plain "collect" — and even a
	// "check" — over a stale QUERY_STORE_TO left in a .env for a feature the
	// operator did not ask for. The refusal belongs where the window's other
	// refusals live, in windowForRun, which knows whether anything is going to
	// read the window.
	rawQSDays := get("QUERY_STORE_DAYS", "")
	rawQSFrom := get("QUERY_STORE_FROM", "")
	rawQSTo := get("QUERY_STORE_TO", "")
	conflict := ""
	if rawQSDays != "" && (rawQSFrom != "" || rawQSTo != "") {
		conflict = "QUERY_STORE_DAYS cannot be combined with QUERY_STORE_FROM or QUERY_STORE_TO: pick a sliding window or an explicit one, not both"
	}
	// The default of 7 applies only when no absolute bound is present. When
	// a bound is present but QUERY_STORE_DAYS was not explicitly set, it is
	// left at 0: resolveWindow treats > 0 as the sliding form and 0 as "use
	// the bounds instead".
	var queryStoreDays int
	switch {
	case rawQSDays != "":
		queryStoreDays = intOf("QUERY_STORE_DAYS", 7, maxQueryStoreDays)
	case rawQSFrom == "" && rawQSTo == "":
		queryStoreDays = 7
	default:
		queryStoreDays = 0
	}
	queryStoreFrom := dateShapeOf("QUERY_STORE_FROM")
	queryStoreTo := dateShapeOf("QUERY_STORE_TO")

	cfg := &Config{
		Server:         get("SQL_SERVER", ""),
		Database:       get("SQL_DATABASE", "master"),
		User:           get("SQL_USER", ""),
		Password:       get("SQL_PASSWORD", ""),
		AppName:        get("SQL_APPLICATION_NAME", DefaultAppName),
		AppNameSet:     set("SQL_APPLICATION_NAME"),
		Integrated:     boolOf("SQL_INTEGRATED_SECURITY", false),
		Encrypt:        boolOf("SQL_ENCRYPT", true),
		TrustCert:      boolOf("SQL_TRUST_SERVER_CERTIFICATE", false),
		ConnectTimeout: secOf("SQL_CONNECT_TIMEOUT_SEC", 15),
		QueryTimeout:   secOf("SQL_QUERY_TIMEOUT_SEC", 60),
		QueriesDir:     get("QUERIES_DIR", ""),
		OutputDir:      get("OUTPUT_DIR", "output"),
		DBInclude:      get("DB_INCLUDE", ""),
		DBExclude:      get("DB_EXCLUDE", ""),

		QueryStoreDays:      queryStoreDays,
		QueryStoreFrom:      queryStoreFrom,
		QueryStoreTo:        queryStoreTo,
		QueryStoreTop:       intOf("QUERY_STORE_TOP", 50, 0),
		QueryStoreDBInclude: get("QUERY_STORE_DB_INCLUDE", ""),

		QueryStoreWindowConflict: conflict,
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if cfg.Server == "" {
		return nil, fmt.Errorf("SQL_SERVER is not set: put it in .env or pass --server")
	}
	if cfg.User != "" && cfg.Password == "" && !cfg.Integrated {
		return nil, fmt.Errorf("SQL_USER is set but SQL_PASSWORD is empty")
	}
	return cfg, nil
}

func checkKeys(dotenv map[string]string) error {
	var bad []string
	for k := range dotenv {
		if knownKeys[k] {
			continue
		}
		if to, ok := renamed[k]; ok {
			return fmt.Errorf("%s is no longer recognised; rename it to %s", k, to)
		}
		bad = append(bad, k)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("unrecognised setting(s): %s", strings.Join(bad, ", "))
	}
	return nil
}
