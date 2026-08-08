package sqlauditor_test

import (
	"runtime/debug"
	"strings"
	"testing"
)

// The godebug directive in go.mod is invisible at every call site: no Go file
// mentions it, so it reads as dead configuration and deleting it breaks
// nothing that anyone can see. This test is the only thing standing between
// that line and a collector that fails against roughly half of all SQL Server
// on Linux instances. CI cannot substitute for it — a green integration run
// only proves that container's certificate serial happened to be positive.
//
// Test binaries inherit the main module's DefaultGODEBUG, so the setting can
// be read straight out of the build info with no build step and no server.
func TestNegativeCertificateSerialIsStillTolerated(t *testing.T) {
	const setting = "x509negativeserial=1"

	const why = "SQL Server on Linux mints its self-signed certificate at startup with a " +
		"random serial number. Whenever the high bit lands set, the DER encodes a negative " +
		"integer, and crypto/x509 has refused to parse that since Go 1.23 — so the TLS " +
		"handshake fails with \"x509: negative serial number\" on roughly half of all fresh " +
		"instances, at random.\n\n" +
		"Nothing in the Go source can fix this. SQL_TRUST_SERVER_CERTIFICATE=true is too " +
		"late, because parsing happens before verification, and SQL_ENCRYPT=false is " +
		"irrelevant, because SQL Server always TLS-wraps the login packet. The godebug " +
		"directive is the whole remedy, and it lives in go.mod so that it is compiled into " +
		"the released binary rather than depending on an environment variable a DBA will " +
		"never set.\n\n" +
		"Restore this line in go.mod:\n\n\tgodebug " + setting

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("debug.ReadBuildInfo failed, so the GODEBUG defaults compiled into this " +
			"binary cannot be read and the negative-serial guard below cannot be checked")
	}

	for _, s := range bi.Settings {
		if s.Key != "DefaultGODEBUG" {
			continue
		}
		if !strings.Contains(s.Value, setting) {
			t.Fatalf("DefaultGODEBUG is %q, which does not contain %q.\n\n%s", s.Value, setting, why)
		}
		return
	}

	t.Fatalf("this binary has no DefaultGODEBUG setting at all, so %q is not in effect. "+
		"That is what removing the godebug line from go.mod looks like.\n\n%s", setting, why)
}
