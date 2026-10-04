package collect

import (
	"context"
	"database/sql"
	"fmt"
)

// skipDroppedDuringRun is the reason given for each unit on a database that
// went away while the run was reading it. scopeLost matches on it, so that the
// comparison with the previous run names the database once.
const skipDroppedDuringRun = "the database was dropped during the collection"

// errDatabaseNotFound is what USE answers for a name that does not resolve.
const errDatabaseNotFound = 911

// databaseDropped reports whether a unit's failure on target means the
// database is no longer on the instance. Databases come and go under a run
// that lasts minutes: an ETL job's staging database, a test database torn
// down by its suite. Seen on the lab: one created and dropped by another job
// during a collection left an error per collector that remained on it, the
// run exited 2, and the day's rerun kept the previous archive as though the
// collection had failed.
//
// The error number alone is not proof. 911 says the name did not resolve for
// this session, and the catalog is what says the database is gone: a 911 on a
// database that is still listed is something else, and stays an error. So is
// a check that could not be made, since nothing then shows the database went
// away.
func databaseDropped(err error, target string, exists func(string) (bool, error)) bool {
	if target == "" || sqlErrorNumber(err) != errDatabaseNotFound {
		return false
	}
	there, cerr := exists(target)
	return cerr == nil && !there
}

// databaseExists asks the catalog, on the collection's own connection, whether
// name is still a database of the instance. DB_ID answers for any database the
// login can see, and the run listed this one from sys.databases at the start.
func databaseExists(ctx context.Context, conn *sql.Conn, cfg *Config, name string) (bool, error) {
	dctx, cancel := deadline(ctx, cfg)
	defer cancel()
	var id sql.NullInt64
	if err := conn.QueryRowContext(dctx, "SELECT DB_ID(@name);", sql.Named("name", name)).Scan(&id); err != nil {
		return false, err
	}
	return id.Valid, nil
}

// droppedBefore is heldBack's twin for a database found dropped: the units
// left on it are skipped rather than sent to a name that no longer resolves.
// Instance-scope units, with no database, never are.
func droppedBefore(on map[string]bool, db string) (string, bool) {
	if db == "" || !on[db] {
		return "", false
	}
	return skipDroppedDuringRun, true
}

// noteDropped records the unit that found db gone as a skip rather than an
// error, marks db so the units after it are skipped too, and says once, in a
// warning, which collector found it gone. It returns what the observer is
// told about the unit.
func noteDropped(m *Manifest, on map[string]bool, script, db string, err error) error {
	on[db] = true
	m.Skipped = append(m.Skipped, SkippedScript{Script: script, Target: db, Reason: skipDroppedDuringRun})
	m.Warnings = append(m.Warnings, fmt.Sprintf(
		"database %s was dropped during the collection: %s found it gone (%v), and its remaining collectors were not run",
		db, script, err))
	return &UnitSkipped{Reason: skipDroppedDuringRun}
}
