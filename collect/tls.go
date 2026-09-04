package collect

import (
	"fmt"
	"strings"
)

// certificateAdvice turns the driver's TLS failure into the decision it
// actually is.
//
// SQL_TRUST_SERVER_CERTIFICATE used to default to true, which encrypts the
// connection and then accepts whatever certificate answers — so anything that
// can put itself between this machine and the instance reads the SQL login and
// its password. The default is false now, and that only helps if the failure it
// produces is legible: an operator who meets "x509: certificate signed by
// unknown authority" and no explanation searches the web, finds
// "TrustServerCertificate=true", and sets it without ever seeing what it costs.
//
// So the message names the setting, says why the failure is expected on a
// default SQL Server installation, and says what turning it on gives up. The
// operator then makes the choice once, knowingly, and it stays recorded in
// their .env rather than in their shell history.
//
// It returns "" for anything that is not a certificate failure — a run that
// cannot resolve the name must not be told to look at TLS — and "" for a run
// that already trusts any certificate, where the certificate is not what went
// wrong.
func certificateAdvice(cfg *Config, err error) string {
	if err == nil || cfg == nil || cfg.TrustCert || !cfg.Encrypt {
		return ""
	}
	msg := strings.ToLower(err.Error())
	// The driver's wording varies with the failure — an unknown issuer, a name
	// that does not match, an expired certificate — and all three arrive as
	// x509 errors under a TLS handshake. Matching on x509 catches them without
	// matching a network error that merely mentions a port.
	if !strings.Contains(msg, "x509") &&
		!strings.Contains(msg, "certificate") &&
		!strings.Contains(msg, "tls handshake") {
		return ""
	}
	// The closing paragraph is written out per case rather than substituted
	// into, and that is a fix rather than a preference. The first version
	// inserted the phrase mid-sentence and pre-wrapped it with a newline of its
	// own, aiming at the width of the paragraph above. No single wrapping suits
	// both phrases, so the most important message this tool prints ended in
	// three ragged lines — measured at 38, 26 and 33 columns against the 74 the
	// rest of it keeps. Someone meeting a security decision for the first time
	// should not be reading something that looks broken.
	exposed := `in your .env — which encrypts the connection and then accepts any
certificate at all, leaving the SQL login and its password readable by
anything that can answer for that address.`
	// No password crosses the wire under Windows authentication, so naming one
	// would overstate the cost and invite the reader to discount the whole
	// warning. What is exposed there is everything the collection carries back.
	if cfg.Integrated || cfg.User == "" {
		exposed = `in your .env — which encrypts the connection and then accepts any
certificate at all, leaving everything this session sends readable by
anything that can answer for that address.`
	}
	return fmt.Sprintf(`the connection was encrypted but this machine does not trust the certificate %s presented.

That is the normal state of a SQL Server installed with its own self-signed
certificate, and it is also exactly what a machine-in-the-middle looks like.
The two are indistinguishable from here, which is why this tool no longer
decides for you.

Either install the instance's certificate in this machine's trust store, or,
if you know the network path between here and that instance, set

    SQL_TRUST_SERVER_CERTIFICATE=true

%s`, cfg.Server, exposed)
}
