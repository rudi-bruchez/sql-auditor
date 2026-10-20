package host

import (
	"strings"
	"testing"
)

const olaCurrent = `$(ESCAPE_SQUOTE(SQLLOGDIR))\$(ESCAPE_SQUOTE(JOBNAME))_$(ESCAPE_SQUOTE(STEPID))_$(ESCAPE_SQUOTE(DATE))_$(ESCAPE_SQUOTE(TIME)).txt`

func TestExpandOutputFile(t *testing.T) {
	logDir := `C:\sqldata\MSSQL\Log`
	ola := TokenValues{SQLLogDir: logDir, JobName: "DatabaseIntegrityCheck - SYSTEM_DATABASES", StepID: 1}
	with := func(f func(*TokenValues)) TokenValues {
		v := ola
		f(&v)
		return v
	}
	tests := []struct {
		name     string
		template string
		values   TokenValues
		readable bool
		dir      string   // checked when readable
		reason   []string // each must appear in Reason
		match    []string
		noMatch  []string
	}{
		{
			name: "ola current", template: olaCurrent, values: ola, readable: true, dir: logDir,
			match:   []string{"DatabaseIntegrityCheck - SYSTEM_DATABASES_1_20261019_185206.txt"},
			noMatch: []string{"DatabaseIntegrityCheck - SYSTEM_DATABASES_2_20261019_185206.txt"},
		},
		{
			name: "apostrophe", template: olaCurrent,
			values:   with(func(v *TokenValues) { v.JobName = "Check de l'instance" }),
			readable: true, dir: logDir,
			match:   []string{"Check de l''instance_1_20261019_185214.txt"},
			noMatch: []string{"Check de l'instance_1_20261019_185214.txt"},
		},
		{
			name:     "ola old",
			template: `$(ESCAPE_SQUOTE(SQLLOGDIR))\DatabaseIntegrityCheck_$(ESCAPE_SQUOTE(JOBID))_$(ESCAPE_SQUOTE(STEPID))_$(ESCAPE_SQUOTE(STRTDT))_$(ESCAPE_SQUOTE(STRTTM)).txt`,
			values:   with(func(v *TokenValues) { v.JobIDBinary = "0x0949CE4BDBFFF742BC01D28BDEF4C9F3" }),
			readable: true, dir: logDir,
			match:   []string{"DatabaseIntegrityCheck_0x0949CE4BDBFFF742BC01D28BDEF4C9F3_1_20260810_3005.txt"},
			noMatch: []string{"DatabaseIntegrityCheck_0x0949CE4BDBFFF742BC01D28BDEF4C9F4_1_20260810_3005.txt"},
		},
		{
			name: "metacharacters", template: olaCurrent,
			values:   with(func(v *TokenValues) { v.JobName = "Nightly [full] *.bak?" }),
			readable: true, dir: logDir,
			match: []string{"Nightly [full] *.bak?_1_20261019_185206.txt"},
			noMatch: []string{
				"Nightly f *.bak?_1_20261019_185206.txt",
				"Nightly [full] .bak?_1_20261019_185206.txt",
				"Nightly [full] *xbak?_1_20261019_185206.txt",
				"Nightly [full] *.ba_1_20261019_185206.txt",
				"Nightly [full] *.bak_1_20261019_185206.txt",
			},
		},
		{
			name: "lowercase is literal", template: `C:\logs\$(jobname).txt`, values: ola,
			readable: true, dir: `C:\logs`,
			match:   []string{"$(jobname).txt"},
			noMatch: []string{"DatabaseIntegrityCheck - SYSTEM_DATABASES.txt", "x.txt"},
		},
		{
			name: "unknown token in name", template: `C:\logs\$(MACH)_x.txt`, values: ola,
			readable: true, dir: `C:\logs`, reason: []string{"MACH"},
			match: []string{"SQL01_x.txt"},
		},
		{
			name: "token in directory", template: `$(SQLDIR)\x.txt`, values: ola,
			reason: []string{"SQLDIR"},
		},
		{
			name: "no SQLLOGDIR value", template: olaCurrent,
			values: with(func(v *TokenValues) { v.SQLLogDir = "" }),
			reason: []string{"SQLLOGDIR unknown before SQL Server 2014"},
		},
		{
			name: "ESCAPE_DQUOTE", template: `C:\logs\$(ESCAPE_DQUOTE(JOBNAME)).txt`,
			values:   with(func(v *TokenValues) { v.JobName = `a"b]c` }),
			readable: true, dir: `C:\logs`,
			match:   []string{`a""b]c.txt`},
			noMatch: []string{`a"b]c.txt`, `a"b]]c.txt`},
		},
		{
			name: "ESCAPE_RBRACKET", template: `C:\logs\$(ESCAPE_RBRACKET(JOBNAME)).txt`,
			values:   with(func(v *TokenValues) { v.JobName = `a"b]c` }),
			readable: true, dir: `C:\logs`,
			match:   []string{`a"b]]c.txt`},
			noMatch: []string{`a"b]c.txt`, `a""b]c.txt`},
		},
		{
			name: "case-insensitive", template: olaCurrent, values: ola, readable: true, dir: logDir,
			match: []string{"databaseintegritycheck - system_databases_1_20261019_185206.TXT"},
		},
		{
			name: "literal close paren", template: `C:\logs\report ($(JOBNAME)).txt`,
			values:   with(func(v *TokenValues) { v.JobName = "Nightly" }),
			readable: true, dir: `C:\logs`,
			match:   []string{"report (Nightly).txt"},
			noMatch: []string{"report (Nightly.txt", "report Nightly).txt"},
		},
		{
			name: "malformed wrapper", template: `C:\logs\$(ESCAPE_SQUOTE(JOBNAME).txt`, values: ola,
			readable: true, dir: `C:\logs`,
			match:   []string{"$(ESCAPE_SQUOTE(JOBNAME).txt"},
			noMatch: []string{"DatabaseIntegrityCheck - SYSTEM_DATABASES.txt", "DatabaseIntegrityCheck - SYSTEM_DATABASES).txt"},
		},
		{
			name: "hyphen and argument tokens", template: `C:\logs\$(A-DBN)_$(WMI(Name)).txt`, values: ola,
			readable: true, dir: `C:\logs`, reason: []string{"A-DBN", "WMI"},
			match: []string{"SALESDB_x.txt"},
		},
		{
			name: "escaped argument token", template: `C:\logs\$(ESCAPE_NONE(WMI(Name)))_x.txt`, values: ola,
			readable: true, dir: `C:\logs`, reason: []string{"WMI"},
			match: []string{"SQL01_x.txt"},
		},
		{
			name: "separator in a value", template: olaCurrent,
			values: with(func(v *TokenValues) { v.JobName = "Nightly/Full" }),
			reason: []string{"separator in JOBNAME"},
		},
		{
			name: "per-run token in directory", template: `$(SQLLOGDIR)\$(DATE)\x.txt`, values: ola,
			reason: []string{"DATE"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ExpandOutputFile(tt.template, tt.values)
			if p.Readable != tt.readable {
				t.Fatalf("Readable = %v, want %v (Reason %q)", p.Readable, tt.readable, p.Reason)
			}
			for _, r := range tt.reason {
				if !strings.Contains(p.Reason, r) {
					t.Errorf("Reason %q does not name %q", p.Reason, r)
				}
			}
			if !tt.readable {
				return
			}
			if p.Dir != tt.dir {
				t.Errorf("Dir = %q, want %q", p.Dir, tt.dir)
			}
			for _, f := range tt.match {
				if !p.Name.MatchString(f) {
					t.Errorf("%q does not match %s", f, p.Name)
				}
			}
			for _, f := range tt.noMatch {
				if p.Name.MatchString(f) {
					t.Errorf("%q matches %s", f, p.Name)
				}
			}
		})
	}
}
