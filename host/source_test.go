package host

import (
	"strings"
	"testing"
)

func TestChooseEventSource(t *testing.T) {
	prod := "PROD"
	tests := []struct {
		name   string
		in     SourceInput
		source string
		rule   string // a prefix
	}{
		{"default instance answered", SourceInput{Answered: true, MachineMatch: true}, "MSSQLSERVER", "instance"},
		{"named instance answered", SourceInput{Answered: true, MachineMatch: true, InstanceName: &prod}, "MSSQL$PROD", "instance"},
		{"env names a local service", SourceInput{EnvServer: `SQL01\PROD`, LocalServices: []string{"MSSQL$PROD", "MSSQL$TEST"}}, "MSSQL$PROD", "env"},
		{"env case differs, service spelling kept", SourceInput{EnvServer: `sql01\prod`, LocalServices: []string{"MSSQL$PROD"}}, "MSSQL$PROD", "env"},
		{"env names an absent service", SourceInput{EnvServer: `SQL01\PROD`, LocalServices: []string{"MSSQL$TEST"}}, "", "undecided"},
		{"listener, one service", SourceInput{EnvServer: "LISTENER01", LocalServices: []string{"MSSQLSERVER", "SQLSERVERAGENT"}}, "MSSQLSERVER", "single service"},
		{"listener, two services", SourceInput{EnvServer: "LISTENER01", LocalServices: []string{"MSSQLSERVER", "MSSQL$TEST"}}, "", "undecided"},
		{"mismatch ignores the instance's name", SourceInput{Answered: true, InstanceName: &prod, EnvServer: "LISTENER01", LocalServices: []string{"MSSQL$A"}}, "MSSQL$A", "single service"},
		{"no service", SourceInput{EnvServer: "LISTENER01"}, "", "undecided"},
		{"other MSSQL services are not engines", SourceInput{EnvServer: "LISTENER01", LocalServices: []string{"MSSQLFDLauncher", "MSSQLLaunchpad$PROD", "MSSQL$MICROSOFT##WID", "MSSQLSERVER"}}, "MSSQLSERVER", "single service"},
		{"service names compared case-insensitively", SourceInput{EnvServer: "LISTENER01", LocalServices: []string{"mssql$prod"}}, "mssql$prod", "single service"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, rule := ChooseEventSource(tc.in)
			if source != tc.source || !strings.HasPrefix(rule, tc.rule) {
				t.Errorf("ChooseEventSource = (%q, %q), want (%q, %q...)", source, rule, tc.source, tc.rule)
			}
		})
	}
}
