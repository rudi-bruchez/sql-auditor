package collect

import (
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
