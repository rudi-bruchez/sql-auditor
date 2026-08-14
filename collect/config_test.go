package collect

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseDotEnv(t *testing.T) {
	in := `
# comment
SQL_SERVER=localhost,1433
export SQL_USER=sa
SQL_PASSWORD="p@ss word"
SQL_DATABASE='master'
EMPTY=
INLINE=value # trailing comment
`
	got, err := ParseDotEnv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseDotEnv: %v", err)
	}
	want := map[string]string{
		"SQL_SERVER":   "localhost,1433",
		"SQL_USER":     "sa",
		"SQL_PASSWORD": "p@ss word",
		"SQL_DATABASE": "master",
		"EMPTY":        "",
		"INLINE":       "value",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	// Flag beats .env beats process environment beats default.
	flags := map[string]string{"SQL_SERVER": "from-flag"}
	// SQL_PASSWORD is present only to satisfy the credential-completeness
	// check; this test is about precedence, not about validation.
	dotenv := map[string]string{
		"SQL_SERVER": "from-dotenv", "SQL_USER": "from-dotenv", "SQL_PASSWORD": "secret",
	}
	environ := func(k string) string {
		switch k {
		case "SQL_SERVER", "SQL_USER":
			return "from-env"
		}
		return ""
	}
	cfg, err := Resolve(flags, dotenv, environ)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Server != "from-flag" {
		t.Errorf("Server = %q, want from-flag", cfg.Server)
	}
	if cfg.User != "from-dotenv" {
		t.Errorf("User = %q, want from-dotenv (.env beats process env by design)", cfg.User)
	}
	if cfg.Database != "master" {
		t.Errorf("Database = %q, want default master", cfg.Database)
	}
	if cfg.ConnectTimeout != 15*time.Second {
		t.Errorf("ConnectTimeout = %v, want 15s", cfg.ConnectTimeout)
	}
}

func TestResolveRejectsUnknownSQLKey(t *testing.T) {
	// SQL_LOGIN is the PowerShell-era typo; it must fail loudly, not fall
	// through to integrated auth.
	_, err := Resolve(nil, map[string]string{"SQL_LOGIN": "sa"}, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected an error for unknown key SQL_LOGIN")
	}
	if !strings.Contains(err.Error(), "SQL_LOGIN") || !strings.Contains(err.Error(), "SQL_USER") {
		t.Errorf("error should name the bad key and the correct one, got: %v", err)
	}
}

func TestResolveRequiresServer(t *testing.T) {
	_, err := Resolve(nil, nil, func(string) string { return "" })
	if err == nil {
		t.Fatal("expected an error when SQL_SERVER is unset")
	}
}

func TestParseDotEnvQuotesAndComments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"hash inside double quotes is preserved", `V="a#b"`, "a#b"},
		{"quoted value followed by trailing comment", `V="secret" # note`, "secret"},
		{"unquoted value with trailing comment", `V=plain # note`, "plain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDotEnv(strings.NewReader(tt.in))
			if err != nil {
				t.Fatalf("ParseDotEnv: %v", err)
			}
			if got["V"] != tt.want {
				t.Errorf("V = %q, want %q", got["V"], tt.want)
			}
		})
	}
}

func TestResolveBoolAndSecValidation(t *testing.T) {
	tests := []struct {
		name       string
		dotenv     map[string]string
		wantErr    bool
		errKey     string
		checkField func(*Config) bool
	}{
		{
			name:   "valid true spellings",
			dotenv: map[string]string{"SQL_ENCRYPT": "yes"},
			checkField: func(c *Config) bool {
				return c.Encrypt == true
			},
		},
		{
			name:   "valid false spellings",
			dotenv: map[string]string{"SQL_ENCRYPT": "off"},
			checkField: func(c *Config) bool {
				return c.Encrypt == false
			},
		},
		{
			name:   "absent key takes the default",
			dotenv: map[string]string{},
			checkField: func(c *Config) bool {
				return c.Encrypt == true && c.ConnectTimeout == 15*time.Second
			},
		},
		{
			name:    "malformed SQL_ENCRYPT errors and names the key",
			dotenv:  map[string]string{"SQL_ENCRYPT": "flase"},
			wantErr: true,
			errKey:  "SQL_ENCRYPT",
		},
		{
			name:    "malformed SQL_CONNECT_TIMEOUT_SEC errors and names the key",
			dotenv:  map[string]string{"SQL_CONNECT_TIMEOUT_SEC": "abc"},
			wantErr: true,
			errKey:  "SQL_CONNECT_TIMEOUT_SEC",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dotenv := map[string]string{"SQL_SERVER": "host"}
			for k, v := range tt.dotenv {
				dotenv[k] = v
			}
			cfg, err := Resolve(nil, dotenv, func(string) string { return "" })
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				if !strings.Contains(err.Error(), tt.errKey) {
					t.Errorf("error should name %s, got: %v", tt.errKey, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !tt.checkField(cfg) {
				t.Errorf("unexpected config: %+v", cfg)
			}
		})
	}
}

func TestResolveQueryStoreDefaults(t *testing.T) {
	cfg, err := Resolve(map[string]string{"SQL_SERVER": "srv"}, nil, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.QueryStoreDays != 7 {
		t.Errorf("QueryStoreDays = %d, want 7", cfg.QueryStoreDays)
	}
	if cfg.QueryStoreTop != 50 {
		t.Errorf("QueryStoreTop = %d, want 50", cfg.QueryStoreTop)
	}
	if cfg.QueryStoreDBInclude != "" {
		t.Errorf("QueryStoreDBInclude = %q, want empty", cfg.QueryStoreDBInclude)
	}
}

func TestResolveQueryStoreOverrides(t *testing.T) {
	dotenv := map[string]string{
		"SQL_SERVER": "srv", "QUERY_STORE_DAYS": "30",
		"QUERY_STORE_TOP": "10", "QUERY_STORE_DB_INCLUDE": "app*",
	}
	cfg, err := Resolve(nil, dotenv, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.QueryStoreDays != 30 || cfg.QueryStoreTop != 10 || cfg.QueryStoreDBInclude != "app*" {
		t.Errorf("got %d/%d/%q, want 30/10/app*",
			cfg.QueryStoreDays, cfg.QueryStoreTop, cfg.QueryStoreDBInclude)
	}
}

// The trap this test exists for: with the default applied unconditionally,
// QueryStoreDays would be 7 here too, and nothing downstream could tell this
// run apart from one that asked for seven days AND a window.
func TestResolveLeavesDaysUnsetWhenBoundsAreGiven(t *testing.T) {
	cfg, err := Resolve(nil, map[string]string{
		"SQL_SERVER": "srv", "QUERY_STORE_FROM": "2026-07-26T14:00",
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.QueryStoreDays != 0 {
		t.Errorf("QueryStoreDays = %d, want 0 — the default must not apply when a bound is given", cfg.QueryStoreDays)
	}
}

// Both window forms at once is a conflict — both readings are defensible and
// guessing is not — but Resolve runs before any flag is read, so refusing here
// killed a plain collect, and a check, over a stale bound in a .env for a
// feature the run never touches. Resolve reports it; windowForRun prices it.
func TestResolveReportsTheDaysBoundsConflictWithoutRefusingIt(t *testing.T) {
	cfg, err := Resolve(nil, map[string]string{
		"SQL_SERVER": "srv", "QUERY_STORE_DAYS": "7",
		"QUERY_STORE_FROM": "2026-07-26T14:00",
	}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve refused a configuration nothing in this run reads: %v", err)
	}
	if cfg.QueryStoreWindowConflict == "" {
		t.Fatal("the conflict was not recorded; only Resolve can see that QUERY_STORE_DAYS was typed rather than defaulted")
	}
	if !strings.Contains(cfg.QueryStoreWindowConflict, "QUERY_STORE_DAYS") {
		t.Errorf("conflict = %q, want it to name the setting", cfg.QueryStoreWindowConflict)
	}
}

// A days count large enough to overflow the duration arithmetic used to produce
// a NEGATIVE window: from landed after to, the extraction matched nothing, and
// the run read as a clean collection of a quiet instance. The value is bounded
// where it is read.
func TestResolveRefusesAnAbsurdQueryStoreDays(t *testing.T) {
	for _, raw := range []string{"999999", strconv.Itoa(maxQueryStoreDays + 1)} {
		_, err := Resolve(nil, map[string]string{"SQL_SERVER": "srv", "QUERY_STORE_DAYS": raw},
			func(string) string { return "" })
		if err == nil {
			t.Errorf("QUERY_STORE_DAYS=%s was accepted; no Query Store holds that history and the "+
				"multiplication into a duration overflows", raw)
		}
	}
	cfg, err := Resolve(nil, map[string]string{
		"SQL_SERVER": "srv", "QUERY_STORE_DAYS": strconv.Itoa(maxQueryStoreDays),
	}, func(string) string { return "" })
	if err != nil || cfg.QueryStoreDays != maxQueryStoreDays {
		t.Errorf("the ceiling itself was refused: %v", err)
	}
}

func TestResolveRefusesNonPositiveQueryStoreDays(t *testing.T) {
	_, err := Resolve(nil, map[string]string{"SQL_SERVER": "srv", "QUERY_STORE_DAYS": "0"},
		func(string) string { return "" })
	if err == nil {
		t.Fatal("Resolve accepted QUERY_STORE_DAYS=0; a window of zero days collects nothing and says nothing")
	}
}

// env init writes the template, and refuses to write over a file that is
// already there. The file it targets is the one holding the operator's server
// and, often, their password: a command whose whole purpose is to produce a
// starting point must never be the thing that destroys the finished one.
func TestWriteEnvTemplateRefusesToOverwriteWithoutForce(t *testing.T) {
	dest := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(dest, []byte("SQL_SERVER=already-here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := WriteEnvTemplate("SQL_SERVER=template\n", dest, false)
	if err == nil {
		t.Fatal("an existing .env was overwritten")
	}
	if !strings.Contains(err.Error(), dest) {
		t.Errorf("error %q does not name the file it refused to touch", err)
	}
	body, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(body) != "SQL_SERVER=already-here\n" {
		t.Errorf("the existing file was modified: %q", body)
	}
}

func TestWriteEnvTemplateWritesVerbatim(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, ".env")
	// Comments and blank lines are the greater part of what makes the template
	// useful, so what lands on disk is the template and not a rendering of the
	// keys it happens to set.
	const body = "# a comment\n\nSQL_SERVER=MYSERVER01\n# SQL_APPLICATION_NAME=sql-auditor\n"
	if err := WriteEnvTemplate(body, dest, false); err != nil {
		t.Fatalf("WriteEnvTemplate: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("wrote %q, want the template verbatim", got)
	}
	// And --force replaces it, which is the only way to get a fresh template
	// next to a .env that has drifted.
	if err := WriteEnvTemplate("SQL_SERVER=second\n", dest, true); err != nil {
		t.Fatalf("WriteEnvTemplate with force: %v", err)
	}
	got, err = os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "SQL_SERVER=second\n" {
		t.Errorf("force did not replace the file: %q", got)
	}
}

// A destination whose directory does not exist is the operator's typo, not an
// invitation to create a tree.
func TestWriteEnvTemplateDoesNotCreateDirectories(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "absent", ".env")
	if err := WriteEnvTemplate("SQL_SERVER=x\n", dest, false); err == nil {
		t.Fatal("a missing parent directory was created")
	}
}
