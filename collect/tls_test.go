package collect

import (
	"errors"
	"strings"
	"testing"
)

// SQL_TRUST_SERVER_CERTIFICATE=true encrypts the connection and then accepts
// any certificate at all, which is the shape a machine-in-the-middle needs to
// read the SQL login and its password off the wire. It was the shipped default
// until 4 September 2026, on the reasoning that most instances present a
// self-signed certificate — true, and not a reason to make every operator
// vulnerable by default rather than the ones who look at it.
func TestTrustServerCertificateIsOffUnlessAsked(t *testing.T) {
	cfg, err := Resolve(nil, map[string]string{"SQL_SERVER": "SQL01"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.TrustCert {
		t.Error("SQL_TRUST_SERVER_CERTIFICATE defaults to true: the login and its password " +
			"are then readable by anything that can answer for the server")
	}
	if !cfg.Encrypt {
		t.Error("SQL_ENCRYPT should stay on by default")
	}
}

// Turning the default off buys nothing if the resulting failure is an opaque
// x509 error: the operator's only move is to search the web, and what they find
// is "set TrustServerCertificate=true". The message has to name the setting and
// what accepting it costs, so the decision is taken knowingly and once.
func TestACertificateFailureExplainsTheChoiceItIs(t *testing.T) {
	cfg := &Config{Server: "SQL01", Encrypt: true, User: "AUDIT_RO"}
	err := errors.New(`TLS Handshake failed: x509: certificate signed by unknown authority`)
	advice := certificateAdvice(cfg, err)
	for _, want := range []string{"SQL_TRUST_SERVER_CERTIFICATE", "self-signed", "password"} {
		if !strings.Contains(advice, want) {
			t.Errorf("the advice should mention %q:\n%s", want, advice)
		}
	}
	// An unrelated failure must not be blamed on TLS.
	if a := certificateAdvice(cfg, errors.New("dial tcp: i/o timeout")); a != "" {
		t.Errorf("a network timeout produced certificate advice:\n%s", a)
	}
	// And neither must a certificate failure on a run that already trusts
	// anything: there the certificate is not what went wrong.
	trusting := &Config{Server: "SQL01", Encrypt: true, TrustCert: true}
	if a := certificateAdvice(trusting, err); a != "" {
		t.Errorf("advice offered to a run that already trusts any certificate:\n%s", a)
	}
}
