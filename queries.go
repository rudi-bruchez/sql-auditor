// Package sqlauditor embeds the SQL corpus shipped with the collector.
package sqlauditor

import "embed"

// Queries holds the diagnostic corpus. The directive deliberately omits the
// all: prefix, so files beginning with "." or "_" — editor swap files, for
// instance — never reach a released binary.
//
//go:embed queries
var Queries embed.FS
