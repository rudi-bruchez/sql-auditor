// Package collect implements the sql-auditor collection pipeline.
package collect

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server, Database, User, Password, AppName string
	Integrated, Encrypt, TrustCert            bool
	ConnectTimeout, QueryTimeout              time.Duration
	QueriesDir, OutputDir                     string
	DBInclude, DBExclude                      string
}

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
}

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
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"',
			len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			v = v[1 : len(v)-1]
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
	boolOf := func(key string, def bool) bool {
		v := strings.ToLower(get(key, ""))
		switch v {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
		return def
	}
	secOf := func(key string, def int) time.Duration {
		n, err := strconv.Atoi(get(key, ""))
		if err != nil || n <= 0 {
			n = def
		}
		return time.Duration(n) * time.Second
	}

	cfg := &Config{
		Server:         get("SQL_SERVER", ""),
		Database:       get("SQL_DATABASE", "master"),
		User:           get("SQL_USER", ""),
		Password:       get("SQL_PASSWORD", ""),
		AppName:        get("SQL_APPLICATION_NAME", "sql-auditor"),
		Integrated:     boolOf("SQL_INTEGRATED_SECURITY", false),
		Encrypt:        boolOf("SQL_ENCRYPT", true),
		TrustCert:      boolOf("SQL_TRUST_SERVER_CERTIFICATE", true),
		ConnectTimeout: secOf("SQL_CONNECT_TIMEOUT_SEC", 15),
		QueryTimeout:   secOf("SQL_QUERY_TIMEOUT_SEC", 60),
		QueriesDir:     get("QUERIES_DIR", ""),
		OutputDir:      get("OUTPUT_DIR", "output"),
		DBInclude:      get("DB_INCLUDE", ""),
		DBExclude:      get("DB_EXCLUDE", ""),
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
