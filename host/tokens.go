package host

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TokenValues are what Agent substitutes for the tokens of one job step.
type TokenValues struct {
	SQLLogDir   string // "" when unknown (SQL Server 2012)
	JobName     string
	StepName    string
	StepID      int
	JobIDBinary string // "0x" + 32 hex digits, from CONVERT(binary(16), job_id)
}

// OutputPattern is where to look for the files a step's output_file_name
// template produced.
type OutputPattern struct {
	Dir      string         // literal directory, tokens expanded
	Name     *regexp.Regexp // anchored, case-insensitive, matches the file name only
	Readable bool
	Reason   string // why not readable, or which tokens became wildcards
}

// agentToken matches $(ESCAPE_X(NAME)), $(ESCAPE_X(NAME(arg))), $(NAME) and
// $(NAME(arg)). Anything else, lowercase or malformed, is literal text.
var agentToken = regexp.MustCompile(`\$\(ESCAPE_([A-Z]+)\(([A-Z][A-Z-]*)(?:\([^()]*\))?\)\)|\$\(([A-Z][A-Z-]*)(?:\([^()]*\))?\)`)

var agentEscapes = map[string]func(string) string{
	"SQUOTE":   func(s string) string { return strings.ReplaceAll(s, "'", "''") },
	"DQUOTE":   func(s string) string { return strings.ReplaceAll(s, `"`, `""`) },
	"RBRACKET": func(s string) string { return strings.ReplaceAll(s, "]", "]]") },
	"NONE":     func(s string) string { return s },
}

// perRun are the tokens whose value changes with each run of the step.
var perRun = map[string]string{
	"DATE":   `\d{8}`,
	"STRTDT": `\d{8}`,
	"TIME":   `\d{1,6}`,
	"STRTTM": `\d{1,6}`,
}

// ExpandOutputFile expands the Agent tokens of an output_file_name template
// into a literal directory and a pattern for the file name. A token whose
// value is unknown at read time becomes a wildcard in the name; in the
// directory it makes the pattern unreadable, since a wildcard directory would
// search the disk.
func ExpandOutputFile(template string, v TokenValues) OutputPattern {
	values := map[string]string{
		"SQLLOGDIR": v.SQLLogDir,
		"JOBNAME":   v.JobName,
		"STEPNAME":  v.StepName,
		"STEPID":    strconv.Itoa(v.StepID),
		"JOBID":     v.JobIDBinary,
	}
	dirTmpl, nameTmpl := "", template
	if i := strings.LastIndexAny(template, `\/`); i >= 0 {
		dirTmpl, nameTmpl = template[:i], template[i+1:]
	}

	// expand walks one part of the template, giving each literal run and
	// each token's value (or wildcard) to emit. It returns why the pattern
	// cannot be read, or "".
	var wildcards []string
	expand := func(s string, inDir bool, emit func(text string, literal bool)) string {
		last := 0
		for _, m := range agentToken.FindAllStringSubmatchIndex(s, -1) {
			var name string
			escape := agentEscapes["NONE"]
			if m[2] >= 0 {
				f, ok := agentEscapes[s[m[2]:m[3]]]
				if !ok {
					continue // unknown escape: literal text
				}
				escape, name = f, s[m[4]:m[5]]
			} else {
				name = s[m[6]:m[7]]
			}
			emit(s[last:m[0]], true)
			last = m[1]
			val, known := values[name]
			switch {
			case known && name == "SQLLOGDIR" && val == "":
				return "SQLLOGDIR unknown before SQL Server 2014"
			case known:
				if strings.ContainsAny(val, `\/`) && !(inDir && name == "SQLLOGDIR") {
					return "separator in " + name
				}
				emit(escape(val), true)
			case inDir:
				return fmt.Sprintf("token %s in the directory", name)
			case perRun[name] != "":
				emit(perRun[name], false)
			default:
				wildcards = append(wildcards, name)
				emit(".*", false)
			}
		}
		emit(s[last:], true)
		return ""
	}

	var dir, re strings.Builder
	if why := expand(dirTmpl, true, func(text string, _ bool) { dir.WriteString(text) }); why != "" {
		return OutputPattern{Reason: why}
	}
	if why := expand(nameTmpl, false, func(text string, literal bool) {
		if literal {
			text = regexp.QuoteMeta(text)
		}
		re.WriteString(text)
	}); why != "" {
		return OutputPattern{Reason: why}
	}
	p := OutputPattern{
		Dir:      dir.String(),
		Name:     regexp.MustCompile(`(?i)^` + re.String() + `$`),
		Readable: true,
	}
	if len(wildcards) > 0 {
		p.Reason = "wildcard for " + strings.Join(wildcards, ", ")
	}
	return p
}
