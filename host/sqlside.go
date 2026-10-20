package host

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// InstanceFacts are what the host read asks the instance about itself.
type InstanceFacts struct {
	ServerName   string
	NetBIOS      string  // ComputerNamePhysicalNetBIOS, for the machine check
	InstanceName *string // nil when NULL (a default instance)
	ErrorLog     string
	Version      string
}

// sqlPreamble starts every query of the host read, as the collector contract
// demands of every collector (collect/queryset_test.go): read uncommitted, and
// a lock wait bounded to 10 s rather than an audit holding msdb behind an open
// transaction. A wait past it is error 1222, the source failed (lockTimedOut).
const sqlPreamble = "SET NOCOUNT ON;\nSET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;\nSET LOCK_TIMEOUT 10000;\n"

const factsSQL = sqlPreamble + `SELECT CONVERT(nvarchar(128), SERVERPROPERTY('ServerName')), CONVERT(nvarchar(128), SERVERPROPERTY('ComputerNamePhysicalNetBIOS')), CONVERT(nvarchar(128), SERVERPROPERTY('InstanceName')), CONVERT(nvarchar(4000), SERVERPROPERTY('ErrorLogFileName')), CONVERT(nvarchar(128), SERVERPROPERTY('ProductVersion'));`

// ReadFacts runs factsSQL. Every column is scanned nullably, as collect.Probe
// does: InstanceName is NULL on a default instance, and a NULL elsewhere costs
// only its own field.
func ReadFacts(ctx context.Context, c *sql.Conn) (InstanceFacts, error) {
	var name, netbios, instance, errorLog, version sql.NullString
	if err := c.QueryRowContext(ctx, factsSQL).Scan(&name, &netbios, &instance, &errorLog, &version); err != nil {
		return InstanceFacts{}, err
	}
	f := InstanceFacts{ServerName: name.String, NetBIOS: netbios.String, ErrorLog: errorLog.String, Version: version.String}
	if instance.Valid {
		f.InstanceName = &instance.String
	}
	return f, nil
}

// JobStep is one Agent job step that writes an output file.
type JobStep struct {
	Job         string
	JobIDBinary string // "0x" + 32 hex digits, what Agent's JOBID token expands to
	StepID      int
	Step        string
	OutputFile  string // the template as typed, tokens included
}

const jobStepsSQL = sqlPreamble + `SELECT j.name, CONVERT(varchar(34), CONVERT(binary(16), j.job_id), 1), s.step_id, s.step_name, s.output_file_name FROM msdb.dbo.sysjobs_view AS j JOIN msdb.dbo.sysjobsteps AS s ON s.job_id = j.job_id WHERE s.output_file_name IS NOT NULL AND s.output_file_name <> N'' ORDER BY j.name, s.step_id OPTION (RECOMPILE, MAXDOP 1);`

// ReadJobSteps runs jobStepsSQL. refused is true when the login may not read
// msdb.dbo.sysjobsteps (sysjobs_view needs SQLAgentReaderRole, and sysjobsteps
// its own grant, see collect/grants.go): the source is then refused, not
// failed. A lock timeout is a failure whose reason says so.
func ReadJobSteps(ctx context.Context, c *sql.Conn) (steps []JobStep, refused bool, err error) {
	rows, err := c.QueryContext(ctx, jobStepsSQL)
	if err != nil {
		return nil, refusal(err), lockTimedOut(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s JobStep
		if err := rows.Scan(&s.Job, &s.JobIDBinary, &s.StepID, &s.Step, &s.OutputFile); err != nil {
			return nil, false, err
		}
		steps = append(steps, s)
	}
	if err := rows.Err(); err != nil {
		return nil, refusal(err), lockTimedOut(err)
	}
	return steps, false, nil
}

// lockTimedOut names error 1222 in err, which alone says only "Lock request
// time out period exceeded": the reason must tell a DBA msdb was held.
func lockTimedOut(err error) error {
	var e mssql.Error
	if errors.As(err, &e) && e.Number == 1222 {
		return fmt.Errorf("lock timeout: msdb stayed locked by another session past SET LOCK_TIMEOUT 10000 (error 1222): %w", err)
	}
	return err
}

// refusal reports a permission error: 229, 297 or 300, the numbers
// collect's blocking watch treats as refusals, and 916, a login that may not
// enter msdb at all.
func refusal(err error) bool {
	var e mssql.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Number {
	case 229, 297, 300, 916:
		return true
	}
	return false
}

// SQLLogDir is the directory part of ErrorLogFileName, what Agent's SQLLOGDIR
// token expands to. The path is a Windows one whichever system parses it.
func SQLLogDir(errorLog string) string {
	i := strings.LastIndex(errorLog, `\`)
	if i < 0 {
		return ""
	}
	return errorLog[:i]
}

// MajorVersion is the leading number of ProductVersion, 0 when there is none.
func MajorVersion(productVersion string) int {
	head, _, _ := strings.Cut(productVersion, ".")
	n, err := strconv.Atoi(strings.TrimSpace(head))
	if err != nil {
		return 0
	}
	return n
}

// Instance is the host read's one connection to the instance, opened on first
// use. A failed open is kept: the run does not dial twice.
type Instance struct {
	cfg  *collect.Config
	db   *sql.DB
	conn *sql.Conn
	err  error
}

func NewInstance(cfg *collect.Config) *Instance { return &Instance{cfg: cfg} }

func (i *Instance) connect(ctx context.Context) (*sql.Conn, error) {
	if i.conn != nil || i.err != nil {
		return i.conn, i.err
	}
	i.db, i.err = collect.Open(i.cfg)
	if i.err != nil {
		return nil, i.err
	}
	// collect.Connect bounds the dial and the login to twice ConnectTimeout.
	i.conn, i.err = collect.Connect(ctx, i.db, i.cfg)
	return i.conn, i.err
}

// Connect opens the connection, collect.Connect bounding the dial and the
// login; Facts and Steps reuse it, or open it themselves when it was not.
func (i *Instance) Connect(ctx context.Context) error {
	_, err := i.connect(ctx)
	return err
}

// Facts reads the instance's facts, nil when it does not answer.
func (i *Instance) Facts(ctx context.Context) (*InstanceFacts, error) {
	c, err := i.connect(ctx)
	if err != nil {
		return nil, err
	}
	f, err := ReadFacts(ctx, c)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Steps reads the job steps on the same connection.
func (i *Instance) Steps(ctx context.Context) ([]JobStep, bool, error) {
	c, err := i.connect(ctx)
	if err != nil {
		return nil, false, err
	}
	return ReadJobSteps(ctx, c)
}

func (i *Instance) Close() {
	if i.conn != nil {
		i.conn.Close()
	}
	if i.db != nil {
		i.db.Close()
	}
}
