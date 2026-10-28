module github.com/rudi-bruchez/sql-auditor

go 1.26

// SQL Server on Linux generates its self-signed certificate at startup with a
// random serial number. When the high bit happens to be set, the DER encodes a
// negative integer, and crypto/x509 has rejected that since Go 1.23 — so the
// TLS handshake fails on roughly half of all freshly started instances, at
// random, with "x509: negative serial number".
//
// Neither configuration knob helps: SQL_TRUST_SERVER_CERTIFICATE=true is too
// late, because parsing happens before verification, and SQL_ENCRYPT=false is
// irrelevant, because SQL Server always TLS-wraps the login packet. The only
// remedy is to accept the malformed serial.
//
// This belongs in go.mod rather than in a GODEBUG environment variable: the
// setting is then compiled into the released binary, and the DBA running it
// against their own server never has to know any of the above.
godebug x509negativeserial=1

require (
	github.com/microsoft/go-mssqldb v1.10.0
	golang.org/x/term v0.45.0
)

require (
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.36.0 // indirect
)
