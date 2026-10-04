package collect

import (
	"fmt"
	"regexp"
	"strings"
)

// The statement scanner behind the query-hint half of the auditor contract.
//
// WHY IT EXISTS. The hint was first enforced by counting: at least as many
// OPTION (RECOMPILE, MAXDOP 1) as declared result sets. That only ever covered
// the statements that return rows. A statement that reads into a #temp table
// or a table variable returns nothing, so nothing required its hint, and on a
// client's large table such a statement goes parallel like any other read.
// Measured on SQL Server 2025, on 5 October 2026: 50.agent/050.commandlog.sql
// buffers its listing into #sel with an unhinted INSERT ... SELECT, and over a
// 200 000-row dbo.CommandLog a default collection compiled it at DOP 22 and
// left the plan in the audited database's Query Store. The count also ran the
// wrong way for the statements that write to work tables: a hinted DELETE or
// UPDATE was counted as a result set, so a file that hinted one failed the lint
// and the files left them bare.
//
// WHAT IT IS. A scan of the code, with comments stripped and literals blanked,
// that tells the statements apart by the one thing T-SQL guarantees about a
// subquery: it is in parentheses. A SELECT, INSERT, UPDATE, DELETE or MERGE at
// parenthesis depth 0 opens a statement; the statement ends at its OPTION
// clause, at a ";", or where the next statement opens. Inside a statement the
// scanner follows the few constructs that put a second keyword at depth 0
// without opening a second statement: the SELECT that feeds an INSERT, the
// SELECT after UNION, EXCEPT or INTERSECT, the main statement after a common
// table expression, the actions of a MERGE, and the CASE ... ELSE ... END of a
// select list.
//
// It is not a parser and does not pretend to be one. Most of the ways it can
// be wrong are loud: a construct it misreads makes it see a statement without
// a hint, or a count of result sets that disagrees, and the file is refused.
// Not all of them. A statement that opens with a parenthesis, such as
// (SELECT ...) UNION (SELECT ...), is not seen as opening one, so an
// unterminated statement before it takes its hint; a review found that form
// and no file of the corpus uses it.

// stmtKind is the keyword a scanned statement opened with.
type stmtKind string

const (
	kindCTE    stmtKind = "WITH" // a WITH not yet followed by its statement
	kindSelect stmtKind = "SELECT"
	kindInsert stmtKind = "INSERT"
	kindUpdate stmtKind = "UPDATE"
	kindDelete stmtKind = "DELETE"
	kindMerge  stmtKind = "MERGE"
)

// scannedStatement is one data statement found by scanDataStatements.
type scannedStatement struct {
	kind       stmtKind
	start, end int // byte offsets of the statement in the scanned text
	hinted     bool
	// otherHint is an OPTION clause not written as the contract writes it.
	otherHint bool
	// assigns is a SELECT @var = ..., into is a SELECT ... INTO: neither
	// returns rows.
	assigns, into bool
	// from is a FROM at depth 0, subquery a SELECT inside parentheses. A
	// SELECT with neither reads no table: SELECT @a = 1, @b = 2.
	from, subquery bool
	// source is how an INSERT gets its rows: "SELECT", "VALUES" or "EXEC".
	source string
	// output is an OUTPUT clause on a DML statement, outputInto one that
	// writes into a table rather than to the client.
	output, outputInto bool
	// cursor is the SELECT of a cursor declaration.
	cursor bool
	// leading is true while the scanner is still before the first item of a
	// SELECT's list.
	leading bool
}

// reads reports whether the statement can read rows, and so whether it can
// be compiled with a parallel plan and must carry the hint.
//
// An INSERT ... EXEC cannot carry one at all: the server refuses an OPTION
// clause there. What it executes is checked on its own, as dynamic SQL, or is
// one of the allowed procedures. An INSERT ... VALUES reads nothing unless a
// subquery stands among the values.
func (s *scannedStatement) reads() bool {
	switch s.kind {
	case kindSelect:
		return s.from || s.subquery || s.emits()
	case kindInsert:
		return s.source == "SELECT" || s.subquery
	case kindUpdate, kindDelete, kindMerge:
		return true
	}
	return false
}

// emits reports whether the statement returns rows to the client, which is
// what a declared result set stands for.
func (s *scannedStatement) emits() bool {
	switch s.kind {
	case kindSelect:
		return !s.assigns && !s.into && !s.cursor
	case kindInsert, kindUpdate, kindDelete, kindMerge:
		return s.output && !s.outputInto
	}
	return false
}

// unhintable is a subquery found where no statement could carry the hint for
// it: DECLARE @x = (SELECT ...), SET @x = (SELECT ...), IF EXISTS (SELECT ...),
// WHILE (SELECT ...) and the like.
type unhintable struct{ at int }

var (
	hintAt = regexp.MustCompile(`(?i)^OPTION\s*\(\s*RECOMPILE\s*,\s*MAXDOP\s+1\s*\)`)
	// optionAt matches an OPTION clause of any content, so the scanner can
	// step over it whole: its parentheses would otherwise be read as a
	// subquery's.
	optionAt = regexp.MustCompile(`(?i)^OPTION\s*\(`)

	// assignOp is what follows the variable of SELECT @x = ..., compound
	// assignments included: SELECT @spent += @pages reads no table either.
	assignOp = regexp.MustCompile(`^\s*[-+*/%&|^]?=`)

	// selectModifiers may stand between SELECT and the first item of its
	// list.
	selectModifiers = map[string]bool{"SELECT": true, "DISTINCT": true, "ALL": true, "TOP": true, "PERCENT": true, "WITH": true, "TIES": true}

	// closers are words that, at depth 0, can only begin a new statement.
	// FETCH is not one: OFFSET ... FETCH NEXT belongs to a SELECT.
	// They end an open one that has no ";" and no OPTION clause, so a hint
	// further down is never credited to it.
	closers = map[string]bool{
		"DECLARE": true, "IF": true, "WHILE": true, "BEGIN": true, "PRINT": true,
		"RAISERROR": true, "THROW": true, "RETURN": true, "CREATE": true,
		"DROP": true, "TRUNCATE": true, "GO": true, "OPEN": true,
		"CLOSE": true, "DEALLOCATE": true, "BREAK": true, "CONTINUE": true,
		"WAITFOR": true, "COMMIT": true, "ROLLBACK": true, "DBCC": true,
	}
)

// scanDataStatements returns the data statements of code, in order, and the
// subqueries that stand outside any of them. code must be comment-stripped and
// string-blanked.
func scanDataStatements(code string) ([]scannedStatement, []unhintable) {
	var (
		out       []scannedStatement
		orphans   []unhintable
		cur       *scannedStatement
		depth     int
		caseDepth int // CASE ... END pairs open at depth 0
		prev      [2]string
	)
	closeAt := func(end int) {
		if cur != nil {
			cur.end = end
			out = append(out, *cur)
			cur = nil
		}
		caseDepth = 0
	}
	open := func(kind stmtKind, at int) {
		closeAt(at)
		cur = &scannedStatement{kind: kind, start: at}
	}
	numberEnd := -1
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == '[':
			// ]] is an escaped bracket inside the name, not its end.
			j := i + 1
			for j < len(code) && (code[j] != ']' || j+1 < len(code) && code[j+1] == ']') {
				if code[j] == ']' {
					j++
				}
				j++
			}
			i = j + 1
			continue
		case c == '"':
			j := strings.IndexByte(code[i+1:], '"')
			if j < 0 {
				i = len(code)
			} else {
				i += j + 2
			}
			continue
		case c == '\'':
			// Literals are blanked, so their contents are spaces; step over
			// the doubled quote of an escape as over any other quote.
			j := strings.IndexByte(code[i+1:], '\'')
			if j < 0 {
				i = len(code)
			} else {
				i += j + 2
			}
			continue
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			closeAt(i)
			prev = [2]string{}
		case isWordByte(c):
			j := i
			if c >= '0' && c <= '9' {
				// A number ends where its digits do: the server reads
				// 1SELECT and 1.SELECT as a number then a keyword.
				hex := strings.HasPrefix(code[i:], "0x") || strings.HasPrefix(code[i:], "0X")
				if hex {
					j += 2
				}
				for j < len(code) && (code[j] >= '0' && code[j] <= '9' || code[j] == '.' ||
					hex && strings.IndexByte("abcdefABCDEF", code[j]) >= 0) {
					j++
				}
				numberEnd = j
			} else {
				for j < len(code) && isWordByte(code[j]) {
					j++
				}
			}
			word := strings.ToUpper(code[i:j])
			// A word after a dot is a column or a method (c.Command,
			// x.value), and a word starting with @ or # is a name.
			qualified := i > 0 && code[i-1] == '.' && i != numberEnd
			if c == '@' || c == '#' || qualified || (c >= '0' && c <= '9') {
				if depth == 0 && cur != nil && cur.leading {
					// The first item of a select list decides whether the
					// statement assigns: SELECT @x = ... returns no rows.
					// TOP's count is a number and does not decide.
					if c == '@' {
						cur.assigns = assignOp.MatchString(code[j:])
						cur.leading = false
					}
				}
				if depth == 0 {
					prev = [2]string{word, prev[0]}
				}
				i = j
				continue
			}
			if depth > 0 {
				if word == "SELECT" {
					if cur == nil {
						orphans = append(orphans, unhintable{at: i})
					} else {
						cur.subquery = true
					}
				}
				i = j
				continue
			}
			if word == "OPTION" && optionAt.MatchString(code[i:]) {
				if cur == nil {
					// A clause with no statement to own it; the server would
					// refuse it, and the hint count has nothing to credit.
					i = j
					continue
				}
				if m := hintAt.FindString(code[i:]); m != "" {
					cur.hinted = true
					i += len(m)
				} else {
					cur.otherHint = true
					i = skipParens(code, i+len(optionAt.FindString(code[i:])))
				}
				closeAt(i)
				prev = [2]string{}
				continue
			}
			if cur != nil && cur.leading && !selectModifiers[word] {
				cur.leading = false
			}
			scanWord(word, i, &cur, &caseDepth, prev, open, closeAt)
			prev = [2]string{word, prev[0]}
			i = j
			continue
		}
		i++
	}
	closeAt(len(code))
	return out, orphans
}

// scanWord applies one depth-0 keyword to the statement being scanned.
func scanWord(word string, at int, cur **scannedStatement, caseDepth *int, prev [2]string,
	open func(stmtKind, int), closeAt func(int)) {
	s := *cur
	switch word {
	case "SELECT":
		switch {
		case prev[0] == "UNION" || prev[0] == "EXCEPT" || prev[0] == "INTERSECT" ||
			(prev[0] == "ALL" && prev[1] == "UNION"):
			// The next branch of the same query.
		case s != nil && s.kind == kindCTE:
			s.kind = kindSelect
			s.leading = true
		case s != nil && s.kind == kindInsert && s.source == "":
			s.source = "SELECT"
		default:
			open(kindSelect, at)
			(*cur).leading = true
			// DECLARE c CURSOR FOR SELECT: the rows go to the cursor and
			// come back through FETCH, never to the client.
			(*cur).cursor = prev[0] == "FOR"
		}
	case "INSERT", "UPDATE", "DELETE", "MERGE":
		kind := stmtKind(word)
		switch {
		case s != nil && s.kind == kindCTE:
			s.kind = kind
		case s != nil && s.kind == kindMerge:
			// WHEN ... THEN INSERT, UPDATE or DELETE: the actions of the MERGE.
		default:
			open(kind, at)
		}
	case "WITH":
		// A common table expression opens a statement; WITH anywhere inside
		// one is a table hint, WITH TIES, WITH ROLLUP and their kind.
		if s == nil {
			open(kindCTE, at)
		}
	case "VALUES":
		if s != nil && s.kind == kindInsert && s.source == "" {
			s.source = "VALUES"
		}
	case "EXEC", "EXECUTE":
		if s != nil && s.kind == kindInsert && s.source == "" {
			s.source = "EXEC"
		} else {
			closeAt(at)
		}
	case "FROM":
		if s != nil {
			s.from = true
		}
	case "INTO":
		if s != nil {
			switch {
			case s.kind == kindSelect:
				s.into = true
			case s.output:
				s.outputInto = true
			}
		}
	case "OUTPUT":
		if s != nil && s.kind != kindSelect && s.kind != kindCTE && s.source == "" {
			s.output = true
		}
	case "SET":
		// UPDATE ... SET and MERGE ... UPDATE SET; anywhere else SET begins
		// a statement of its own.
		if s != nil && s.kind != kindUpdate && s.kind != kindMerge {
			closeAt(at)
		}
	case "CASE":
		if s != nil {
			*caseDepth++
		}
	case "END":
		if s != nil && *caseDepth > 0 {
			*caseDepth--
		} else {
			closeAt(at)
		}
	case "ELSE":
		if s == nil || *caseDepth == 0 {
			closeAt(at)
		}
	default:
		if closers[word] {
			closeAt(at)
		}
	}
}

func isWordByte(c byte) bool {
	return c == '_' || c == '@' || c == '#' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}

// skipParens returns the offset just past the parenthesis that closes the one
// already opened before i.
func skipParens(code string, i int) int {
	for depth := 1; i < len(code); i++ {
		switch code[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(code)
}

// hintLint is the first half of the hint rule: every statement that can read
// rows carries OPTION (RECOMPILE, MAXDOP 1), written exactly so, and no read
// stands where no statement could carry it. It returns the statements, so the
// caller can count the ones that return rows.
func hintLint(code string) ([]scannedStatement, string) {
	stmts, orphans := scanDataStatements(code)
	for _, s := range stmts {
		if !s.reads() || s.hinted {
			continue
		}
		if s.otherHint {
			return stmts, fmt.Sprintf("every statement that reads rows needs OPTION (RECOMPILE, MAXDOP 1) written exactly so, "+
				"and this one has an OPTION clause in another form: %s", excerpt(code[s.start:s.end]))
		}
		return stmts, fmt.Sprintf("every statement that reads rows needs OPTION (RECOMPILE, MAXDOP 1), "+
			"or the server may give it a parallel plan on a large table of the instance it audits; none on: %s",
			excerpt(code[s.start:s.end]))
	}
	if len(orphans) > 0 {
		at := orphans[0].at
		return stmts, fmt.Sprintf("a subquery in a DECLARE, SET, IF or WHILE cannot carry OPTION (RECOMPILE, MAXDOP 1): "+
			"assign it with SELECT @var = ... OPTION (RECOMPILE, MAXDOP 1) and test the variable; found: %s",
			excerpt(code[max(0, lineStart(code, at)):]))
	}
	return stmts, ""
}

// lineStart returns the offset of the start of the line holding at.
func lineStart(code string, at int) int {
	return strings.LastIndexByte(code[:at], '\n') + 1
}

// excerpt is the opening of a statement, on one line, short enough for a
// lint message to name it.
func excerpt(stmt string) string {
	s := strings.Join(strings.Fields(stmt), " ")
	if len(s) > 80 {
		s = s[:80] + " ..."
	}
	return s
}

// EmittingStatements returns the text of each statement of a collector that
// returns rows to the client, in order, which is the order of its declared
// result sets. sql is the script as written; comments are stripped and
// literals blanked here, so dynamic SQL, which this program reads into
// table variables rather than emits, is not counted.
func EmittingStatements(sql string) []string {
	code := BlankSQLStrings(normalizeSeparators(StripSQLComments(sql)))
	stmts, _ := scanDataStatements(code)
	var out []string
	for _, s := range stmts {
		if s.emits() {
			out = append(out, code[s.start:s.end])
		}
	}
	return out
}
