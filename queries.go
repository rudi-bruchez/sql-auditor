// Package sqlauditor embeds the SQL corpus shipped with the collector.
package sqlauditor

import "embed"

// Queries holds the diagnostic corpus. The directive deliberately omits the
// all: prefix, so files beginning with "." or "_" — editor swap files, for
// instance — never reach a released binary.
//
//go:embed queries
var Queries embed.FS

// EnvExample is the annotated configuration template, embedded so that
// `sql-auditor env init` can write it on a machine that has the executable and
// nothing else. That is the ordinary case for the person who runs this: the
// binary arrives on its own, the key set is closed, and a .env written from
// memory is refused. The file is the documentation of that set, so it ships
// inside the thing it documents.
//
// An explicit filename embeds a dot-prefixed file that a directory pattern
// would skip, which is why this cannot simply be part of the corpus above.
//
//go:embed .env.example
var EnvExample string
