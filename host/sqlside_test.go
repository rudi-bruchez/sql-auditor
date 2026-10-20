package host

import (
	"errors"
	"fmt"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

func TestSQLLogDir(t *testing.T) {
	for in, want := range map[string]string{
		`C:\sqldata\MSSQL17.MSSQLSERVER\MSSQL\Log\ERRORLOG`: `C:\sqldata\MSSQL17.MSSQLSERVER\MSSQL\Log`,
		"":                            "",
		"/var/opt/mssql/log/errorlog": "",
	} {
		if got := SQLLogDir(in); got != want {
			t.Errorf("SQLLogDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	for in, want := range map[string]int{"17.0.5015.4": 17, "11.0.7001.0": 11, "": 0} {
		if got := MajorVersion(in); got != want {
			t.Errorf("MajorVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRefusalNumbers(t *testing.T) {
	if !refusal(fmt.Errorf("job steps: %w", mssql.Error{Number: 229})) {
		t.Errorf("error 229 is not a refusal")
	}
	for _, n := range []int32{297, 300, 916} {
		if !refusal(mssql.Error{Number: n}) {
			t.Errorf("error %d is not a refusal", n)
		}
	}
	if refusal(mssql.Error{Number: 208}) {
		t.Errorf("error 208 is a refusal")
	}
	if refusal(errors.New("connection reset")) {
		t.Errorf("an error that is not mssql.Error is a refusal")
	}
}
