package collect

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The statement-class rule, and why it exists.
//
// Everything else this program says about itself rests on one claim: a
// collector reads and never writes. MANIFEST.txt states it in so many words —
// "nothing that belongs to this server or its databases is created, altered or
// deleted" — and a security officer releases the archive on the strength of it.
//
// Until this file existed, nothing enforced it. contractLint checked the shape
// of a collector (NOCOUNT, the isolation level, the lock timeout, one query
// hint per declared result set) and never once looked at what the statements
// did. A file that met all of that and dropped a database passed the lint and
// ran, with the full privileges of the configured login, while the run reported
// "0 error(s)" and exit 0 and the manifest went on attesting that nothing had
// been altered. That was demonstrated rather than argued: an external reviewer
// wrote such a corpus, pointed --queries-dir at it, and lost a database.
//
// The default path was never exposed — the embedded corpus is this project's
// own — but "queries export --to DIR" exists precisely so that people edit the
// corpus, and a DBA's own script collection routinely holds DBCC, KILL and
// index rebuilds. The gap was between "this project ships read-only queries"
// and "this program runs read-only queries", and it is the second one the
// manifest promises.
//
// WHAT THIS IS NOT. It is not a sandbox and it cannot be one: whoever can pass
// --queries-dir can also open sqlcmd and type the same statement. Nothing here
// stops a determined author. What it stops is the accident — the maintenance
// script pasted into the corpus directory — and it makes the manifest's
// attestation something the program checks rather than something it asserts,
// because a corpus that fails this lint does not run at all.

var (
	// Read-only DBCC commands. DBCC is the one place where a genuine read is
	// spelled as a command, which is why the corpus runs TRACESTATUS and why
	// the manifest mentions DBCC by name; the rest of the DBCC surface frees
	// caches, shrinks files, reseeds identities and repairs pages. So the
	// command word is allowlisted rather than the statement keyword.
	readOnlyDBCC = map[string]bool{
		"TRACESTATUS":     true,
		"SHOW_STATISTICS": true,
		"SQLPERF":         true,
		"INPUTBUFFER":     true,
		"OPENTRAN":        true,
		"USEROPTIONS":     true,
		"PROCCACHE":       true,
		"LOGINFO":         true,
		"PAGE":            true,
	}

	// Statement keywords no collector may use in any form.
	forbiddenOutright = regexp.MustCompile(`(?i)\b(ALTER|BACKUP|RESTORE|GRANT|REVOKE|DENY|KILL|MERGE|RECONFIGURE|SHUTDOWN|WRITETEXT|UPDATETEXT|OPENROWSET|OPENQUERY|OPENDATASOURCE|BULK\s+INSERT|SETUSER)\b`)

	// EXECUTE AS switches the security context the rest of the batch runs
	// under, which is the one thing that would make every other rule here
	// negotiable. Matched separately because EXECUTE on its own is ordinary.
	forbiddenImpersonation = regexp.MustCompile(`(?i)\bEXEC(UTE)?\s+AS\b`)

	// Procedures that write. xp_ is refused wholesale — the extended procedure
	// surface reaches the registry, the file system and the shell, and no
	// collector needs it; the corpus reads the error log through
	// sys.sp_readerrorlog rather than through xp_readerrorlog for exactly this
	// reason. The sp_ names are the ones that change the instance.
	forbiddenProcedure = regexp.MustCompile(`(?i)\b(xp_[a-z0-9_]+|sp_configure|sp_addlogin|sp_droplogin|sp_addsrvrolemember|sp_addrolemember|sp_add_job|sp_start_job|sp_stop_job|sp_delete_job|sp_setapprole|sp_send_dbmail|sp_OACreate|sp_OAMethod|sp_attach_db|sp_detach_db|sp_dropserver|sp_addlinkedserver|sp_add_jobstep|sp_update_job)\b`)

	// The statements a collector may use, but only against session-scoped
	// scratch: a table variable or a #temp table, both of which die with the
	// connection. The corpus needs them because several DMFs and procedures
	// return rows that have to be captured before they can be joined.
	scopedStatement = regexp.MustCompile(`(?i)\b(CREATE\s+TABLE|DROP\s+TABLE|TRUNCATE\s+TABLE|INSERT(\s+INTO)?|DELETE(\s+FROM)?|UPDATE)\s+(IF\s+EXISTS\s+)?\[?[@#]`)
	scopedKeyword   = regexp.MustCompile(`(?i)\b(CREATE|DROP|TRUNCATE|INSERT|DELETE|UPDATE)\b`)

	// SELECT ... INTO creates a table, and a permanent one unless the target
	// starts with #. INSERT INTO is the same word in a different statement, so
	// the two are told apart by counting rather than by looking backwards.
	intoTarget   = regexp.MustCompile(`(?i)\bINTO\s+\[?[@#]`)
	intoKeyword  = regexp.MustCompile(`(?i)\bINTO\b`)
	insertIntoKw = regexp.MustCompile(`(?i)\bINSERT\s+INTO\b`)

	dbccStatement = regexp.MustCompile(`(?i)\bDBCC\s+([a-z_]+)`)
	dbccKeyword   = regexp.MustCompile(`(?i)\bDBCC\b`)

	// Dynamic SQL has to be a literal this lint can read. A statement
	// assembled from variables is unreviewable by construction, and every rule
	// above would then be one string concatenation away from nothing.
	execOfVariable = regexp.MustCompile(`(?i)\bEXEC(UTE)?\s*\([^)]*@`)
	execsqlOfLit   = regexp.MustCompile(`(?i)\bsp_executesql\s+N?\s*'`)
	execsqlKeyword = regexp.MustCompile(`(?i)\bsp_executesql\b`)
)

// maxDynamicDepth bounds the recursion into nested dynamic SQL. Three levels is
// already one more than any legitimate collector has needed; the bound exists
// so a pathological file cannot spend the linter's stack.
const maxDynamicDepth = 3

// statementLint refuses a collector that could change the server.
//
// sql must already have had its comments stripped, or a file explaining why it
// drops nothing would be refused for saying so.
//
// It runs over the same text twice. Once over the code with literals blanked,
// which is where a foreign script writes its statements plainly; and once over
// the contents of every literal that is handed to EXEC or sp_executesql, which
// is where this corpus's own guard pattern keeps its SELECTs — and where a
// destructive statement would hide. A literal that is only selected, compared
// or concatenated is left alone, because a CASE arm reading 'DBCC fixed the
// page' is data. That distinction is the reason this cannot be a grep.
func statementLint(sql string) string {
	return statementLintDepth(sql, 0)
}

func statementLintDepth(sql string, depth int) string {
	if msg := scanStatements(BlankSQLStrings(sql)); msg != "" {
		return msg
	}
	lits := executedLiterals(sql)
	if len(lits) > 0 && depth >= maxDynamicDepth {
		return "dynamic SQL nested more than three deep: a collector this indirect cannot be reviewed for what it does to the server"
	}
	for _, lit := range lits {
		if msg := statementLintDepth(lit, depth+1); msg != "" {
			return msg + " — inside the dynamic SQL this file executes"
		}
	}
	return ""
}

// scanStatements applies the rules to one piece of code whose literals have
// already been blanked.
//
// The scoped statements and SELECT ... INTO are checked by counting rather than
// by matching each occurrence in place: the permitted form is a strict subset
// of the keyword's matches, so "more keywords than permitted forms" is exactly
// "at least one that is not permitted", and it stays true whatever whitespace
// or bracketing the author used.
func scanStatements(code string) string {
	if m := forbiddenOutright.FindString(code); m != "" {
		return fmt.Sprintf("%s is not a read: a collector may only issue SELECT, and the archive's manifest attests that nothing on the server is created, altered or deleted",
			strings.ToUpper(strings.Join(strings.Fields(m), " ")))
	}
	if forbiddenImpersonation.MatchString(code) {
		return "EXECUTE AS changes the security context the rest of the batch runs under, which would make every other rule in this lint negotiable"
	}
	if m := forbiddenProcedure.FindString(code); m != "" {
		return fmt.Sprintf("%s changes the instance rather than reading it: a collector may only call procedures that return rows", m)
	}
	if n := countAll(scopedKeyword, code); n > countAll(scopedStatement, code) {
		return fmt.Sprintf("%s: a collector may only write to a table variable or a #temp table, which die with the connection — anything else belongs to the server it is auditing",
			strings.ToUpper(scopedKeyword.FindString(code)))
	}
	if countAll(intoKeyword, code) > countAll(intoTarget, code)+countAll(insertIntoKw, code) {
		return "SELECT ... INTO creates a table: a collector may only materialise into a table variable or a #temp table"
	}
	if dbccKeyword.MatchString(code) {
		for _, m := range dbccStatement.FindAllStringSubmatch(code, -1) {
			if !readOnlyDBCC[strings.ToUpper(m[1])] {
				return fmt.Sprintf("DBCC %s is not one of the read-only DBCC commands (%s)", m[1], readOnlyDBCCList())
			}
		}
		if countAll(dbccKeyword, code) > len(dbccStatement.FindAllString(code, -1)) {
			return "DBCC without a command word: this lint has to be able to name the command before it can say whether it reads"
		}
	}
	if execOfVariable.MatchString(code) {
		return "dynamic SQL assembled from variables: a statement this lint cannot read is one it cannot vouch for, so EXEC takes a literal here"
	}
	if countAll(execsqlKeyword, code) > countAll(execsqlOfLit, code) {
		return "sp_executesql given a statement assembled from variables: its first argument must be a literal this lint can read"
	}
	return ""
}

func countAll(re *regexp.Regexp, s string) int { return len(re.FindAllString(s, -1)) }

// readOnlyDBCCList is sorted so the message a maintainer sees is the same every
// time; Go's map iteration order is not.
func readOnlyDBCCList() string {
	names := make([]string, 0, len(readOnlyDBCC))
	for n := range readOnlyDBCC {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// executedLiterals returns the contents of every string literal handed to EXEC
// or sp_executesql, with doubled quotes unescaped, so the caller can lint it as
// the code it is.
func executedLiterals(sql string) []string {
	cls := classifySQL(sql)
	var out []string
	for i := 0; i < len(sql); {
		if cls[i] != clsLiteral {
			i++
			continue
		}
		start := i
		for i < len(sql) && cls[i] == clsLiteral {
			i++
		}
		if isExecuted(sql, start) {
			out = append(out, strings.ReplaceAll(sql[start:i], "''", "'"))
		}
		// Step over the closing delimiter, which classifySQL leaves as code.
		i++
	}
	return out
}

var (
	execOpen  = regexp.MustCompile(`(?i)\bEXEC(UTE)?\s*\(\s*$`)
	execsqlAt = regexp.MustCompile(`(?i)\bsp_executesql$`)
)

// isExecuted reports whether the literal whose contents begin at start is the
// argument of a dynamic-execution construct, by reading the code before the
// opening quote. An N prefix is dropped first: every dynamic statement in this
// corpus is an nvarchar literal.
func isExecuted(sql string, start int) bool {
	j := start - 1
	if j < 0 || sql[j] != '\'' {
		return false
	}
	before := strings.TrimRight(sql[:j], " \t\r\n")
	if n := len(before); n > 0 && (before[n-1] == 'N' || before[n-1] == 'n') {
		before = strings.TrimRight(before[:n-1], " \t\r\n")
	}
	return execOpen.MatchString(before) || execsqlAt.MatchString(before)
}
