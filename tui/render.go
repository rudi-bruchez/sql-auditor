package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

// Render paints one frame: the whole screen, as lines, for a state and a
// terminal size. It is THE rendering boundary — the terminal layer owns the
// only io.Writer in the wizard and does nothing but write the lines it is
// handed, and there is no second rendering signature anywhere.
//
// That is what makes every screen testable without a terminal, without a
// clock and without a connection, which in a repository whose tests never open
// a connection is the difference between covered and not covered.
//
// It is total on its inputs. GetSize reports 0x0 while an RDP window is being
// dragged and the loop keeps repainting through it, so every width computation
// below has to survive a zero — a panic in the renderer leaves the terminal in
// raw mode with the wizard gone.
func Render(s State, width, height int) []string {
	var lines []string
	switch s.Step {
	case StepConnection:
		lines = renderConnection(s, width)
	case StepConnecting:
		lines = renderConnecting(s, width)
	case StepVerification:
		lines = renderVerification(s, width)
	case StepVerifying:
		lines = renderVerifying(s, width)
	case StepOptions:
		lines = renderOptions(s, width)
	case StepCollecting:
		lines = renderCollecting(s, width)
	case StepDone:
		lines = renderDone(s, width)
	}
	if s.ASCII {
		// The last transformation, applied once to the finished frame rather
		// than at the twenty places a collect text enters it: a console whose
		// output code page could not be moved to UTF-8 would otherwise draw the
		// em dash of an Impact as two bytes of mojibake.
		for i, l := range lines {
			lines[i] = screen.ToASCII(l)
		}
	}
	// A frame longer than the terminal is cut at the bottom rather than
	// scrolled: there is no back-scroll in this wizard, and a frame that
	// overflowed would push its own header off the top of the screen.
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return lines
}

// The two indents every screen is built from, and the width of the status
// column that "ok", "denied", "error" and "not checked" share. Nine columns
// because "denied" plus a gap is what the mock-ups align on, and the impact of
// a denied capability hangs under its name rather than under its status.
const (
	pad         = "  "
	fieldPad    = "    "
	statusWidth = 9
	impactPad   = "             " // len(fieldPad) + statusWidth
	rightMargin = 2
)

// The four words a capability line can start with. They carry the whole
// meaning: there is no colour anywhere in this package, which is what makes one
// set of witness strings enough and NO_COLOR trivially honoured.
const (
	statusNotChecked = "not checked"
)

// row places a right-hand value at the right edge of the terminal, which is
// where the mock-ups put the step number and the counters. It keeps a two
// column margin: a line filled to the exact width wraps on its own on several
// terminals, and a wrapped header is a frame that no longer matches its size.
func row(left, right string, width int) string {
	if right == "" {
		return left
	}
	gap := width - rightMargin - utf8.RuneCountInString(left) - utf8.RuneCountInString(right)
	if gap < 2 {
		// Too narrow to align: keep both values and let the line be long. The
		// alternative is dropping one of them, and the one on the right is the
		// counter.
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + right
}

// header is the cartouche of screen 1: version AND build hash. Not decoration.
// check and collect print the same pair so an archive, a terminal transcript
// and a bug report can be tied to the build that produced them — and a
// screenshot of this wizard is the terminal transcript that will be attached
// to the report.
func header(s State, step string, width int) string {
	name := "sql-auditor"
	if s.Version != "" {
		name += " " + s.Version
	}
	if s.Build != "" {
		name += " (" + s.Build + ")"
	}
	return row(pad+name, step, width)
}

// spinner is the activity indicator of the two waiting steps. Four ASCII
// frames, advanced by the ticker: something has to move while the wizard waits
// on a server that may never answer, and a frozen screen in front of a frozen
// instance is the exact symptom this package exists to remove.
func spinner(phase int) string {
	frames := []string{"|", "/", "-", "\\"}
	if phase < 0 {
		phase = -phase
	}
	return frames[phase%len(frames)]
}

// ---------------------------------------------------------------- screen 1

func renderConnection(s State, width int) []string {
	out := []string{
		header(s, "step 1/4", width),
		"",
		pad + "Connection",
		"",
		fieldPad + fmt.Sprintf("%-11s%s", "Server", editable(s.Server, s.Field == fieldServer)) + overrideNote(s),
		fieldPad + fmt.Sprintf("%-11s%s", "Database", s.Catalog),
		fieldPad + fmt.Sprintf("%-11s%s", "Auth", authLine(s)),
		fieldPad + fmt.Sprintf("%-11s%s", "Password", editable(mask(s.Password), s.Field == fieldPassword)),
		fieldPad + fmt.Sprintf("%-11s%s", "Encrypted", encryptionLine(s)),
	}
	if s.ConnError != nil {
		// The one error in this wizard that does not end a step. The server's
		// own words, unedited: "login failed for user" and "no such host" send
		// the operator to two different places, and a message of ours would
		// have to guess which.
		out = append(out, "")
		out = append(out, screen.Wrap(s.ConnError.Error(), width, fieldPad)...)
	}
	if s.Source != "" {
		out = append(out, "", fieldPad+s.Source)
	}
	// [esc] rather than [q], and this is the one screen where they differ.
	// Every printable rune belongs to the field being edited: a server named
	// QUALIF, or a password with a q in it, must be typeable.
	return append(out, "", pad+"[enter] connect   [tab] next field   [esc] quit")
}

// editable draws one of the two editable values, underscoring the rest of the
// field when it has the cursor. The underscores are the whole of the focus
// indicator: there is no colour, and a cursor the terminal places would be
// invisible in a screenshot.
func editable(v string, focused bool) string {
	const fieldWidth = 32
	if !focused {
		return v
	}
	n := fieldWidth - utf8.RuneCountInString(v)
	if n < 1 {
		n = 1
	}
	return v + strings.Repeat("_", n)
}

// mask hides the password with a fixed ASCII character. The count of stars
// follows the count of runes so an operator can see that a keystroke landed —
// on a field they cannot read back, an edit that does nothing visible is
// indistinguishable from a dead keyboard.
//
// The value itself never reaches a line: it is not written to disk, not put in
// the manifest, and a test in this package asserts it appears in no rendered
// line of any screen.
func mask(p string) string { return strings.Repeat("*", utf8.RuneCountInString(p)) }

// overrideNote marks a field typed over a value that came from --server. The
// typing wins, because it is the last thing the operator did; saying so is what
// keeps somebody who typed a command line half a minute ago from being
// surprised about where the connection went.
func overrideNote(s State) string {
	if s.ServerOverridden {
		return "  (overrides --server)"
	}
	return ""
}

func authLine(s State) string {
	if s.Integrated {
		if s.User != "" {
			return "Windows integrated  " + s.User
		}
		return "Windows integrated"
	}
	return "SQL login  " + s.User
}

// encryptionLine spells out what the connection actually does, including the
// part that sounds reassuring and is not: TRUST_SERVER_CERTIFICATE encrypts the
// traffic and authenticates nobody, so a screen that said only "yes" would let
// a reader believe the certificate was checked.
func encryptionLine(s State) string {
	if !s.Encrypt {
		return "no"
	}
	if s.TrustCert {
		return "yes, server certificate NOT validated"
	}
	return "yes, server certificate validated"
}

func renderConnecting(s State, width int) []string {
	out := []string{
		header(s, "step 1/4", width),
		"",
		pad + "Connecting",
		"",
	}
	out = append(out, screen.Wrap(spinner(s.Spinner)+" opening a connection to "+s.Server, width, fieldPad)...)
	return append(out, "", pad+"[ctrl-c] cancel")
}

// ---------------------------------------------------------------- screen 2

func renderVerifying(s State, width int) []string {
	out := []string{
		row(pad+"Verifying", "step 2/4", width),
		"",
	}
	// Naming the four probes is not padding. Verify bounds each of them by
	// SQL_QUERY_TIMEOUT_SEC, so a frozen instance can hold this screen for
	// minutes, and an operator who knows what is being waited on can decide
	// whether to wait.
	out = append(out, screen.Wrap(spinner(s.Spinner)+" probing the instance: permissions, server version, "+
		"databases, blocked process capture", width, fieldPad)...)
	return append(out, "", pad+"[ctrl-c] cancel")
}

func renderVerification(s State, width int) []string {
	out := []string{row(pad+"Verification", "step 2/4", width), ""}
	out = append(out, serverBlock(s, width)...)
	out = append(out, "")
	out = append(out, permissionBlock(s, width)...)
	out = append(out, "")
	out = append(out, databaseBlock(s, width)...)
	out = append(out, "")
	out = append(out, blockingBlock(s, width)...)
	out = append(out, "")
	if !s.canContinue() {
		// The continuation is not merely undrawn, it is gone: Key refuses it
		// too. A collection whose coverage is unknown is what this screen
		// exists to prevent, and the gauge's denominator would be wrong anyway.
		return append(out, pad+"[r] re-probe   [b] back   [q] quit")
	}
	return append(out, pad+"[enter] continue   [r] re-probe   [b] back   [q] quit")
}

func serverBlock(s State, width int) []string {
	if !s.Verify.Probed {
		out := []string{row(pad+"Server", statusNotChecked, width)}
		if s.Verify.ServerErr != nil {
			out = append(out, screen.Wrap(s.Verify.ServerErr.Error(), width, fieldPad)...)
		}
		return out
	}
	return []string{
		pad + fmt.Sprintf("%-9s%s   %s  %s", "Server", s.Verify.Server.Name, s.Verify.Server.Version, s.Verify.Server.Edition),
		pad + fmt.Sprintf("%-9s%s", "Login", s.Verify.Server.Login),
	}
}

// permissionBlock lists ALL eight capabilities, in the order Capabilities()
// probes them, with the total derived from that same slice rather than written
// down here. Writing "8" would be a number this screen invented, and the
// repository's rule is that nothing on screen is invented: it would also
// survive the day a ninth capability is added, silently.
//
// An "ok" fits on one line; a "denied" or "error" is followed by its Impact,
// FOLDED and never truncated. Cutting "the report must not read this as 'no log
// shipping'" after "no lo…" reverses its meaning, and this screen is the only
// place the operator will ever read it.
func permissionBlock(s State, width int) []string {
	caps := collect.Capabilities()
	byName := map[string]collect.CapabilityCheck{}
	for _, c := range s.Verify.Checks {
		byName[c.Name] = c
	}

	ok := 0
	var body []string
	for _, c := range caps {
		check, found := byName[c.Name]
		// Not probed means not known. "ok" would be a claim about an instance
		// nothing spoke to, and "denied" would send a DBA hunting a GRANT that
		// was never the problem.
		if !s.Verify.Probed || !found {
			body = append(body, fieldPad+status(statusNotChecked)+c.Name)
			continue
		}
		body = append(body, fieldPad+status(check.Status)+c.Name)
		if check.Status == "ok" {
			ok++
			continue
		}
		// Verbatim from the capability that stated it, folded under the name.
		body = append(body, screen.Wrap(check.Impact, width, impactPad)...)
	}

	count := fmt.Sprintf("%d / %d", ok, len(caps))
	if !s.Verify.Probed {
		count = statusNotChecked
	}
	out := append([]string{row(pad+"Permissions", count, width)}, body...)
	out = append(out, "", fieldPad+"[g] write the T-SQL that grants exactly what is missing")
	switch {
	case s.GrantError != nil:
		out = append(out, screen.Wrap("could not write it: "+s.GrantError.Error(), width, fieldPad)...)
	case s.GrantPath != "":
		// The path on its own line, as on the final screen: it is there to be
		// selected with one drag of the mouse and pasted into a mail.
		out = append(out, fieldPad+"written to:", fieldPad+"  "+s.GrantPath)
	}
	return out
}

// status pads a status word into the column the impacts hang under. "not
// checked" is wider than the column and keeps one space rather than losing it,
// because an alignment that swallows the gap joins two words into one.
func status(st string) string {
	if len(st) >= statusWidth {
		return st + " "
	}
	return st + strings.Repeat(" ", statusWidth-len(st))
}

// databaseBlock names the databases the login cannot read. Naming them is the
// point: they are two thirds of the collectors, and the permission probes
// cannot see them at all, since every probe reads at server level.
func databaseBlock(s State, width int) []string {
	if !s.Verify.Probed || s.Verify.CandidatesErr != nil {
		out := []string{row(pad+"Databases", statusNotChecked, width)}
		if s.Verify.CandidatesErr != nil {
			out = append(out, screen.Wrap(s.Verify.CandidatesErr.Error(), width, fieldPad)...)
		}
		return out
	}
	out := []string{row(pad+"Databases",
		fmt.Sprintf("%d selected, %d skipped", len(s.Verify.Selection.Included), len(s.Verify.Selection.Skipped)), width)}
	if len(s.Verify.NoAccess) > 0 {
		// Named one by one, wrapped: they cost two thirds of the collectors,
		// and a count would leave the reader unable to tell which databases the
		// grant script has to cover.
		out = append(out, hang(fieldPad+"no access", 16, strings.Join(s.Verify.NoAccess, ", "), width)...)
	}
	return out
}

// blockingBlock is written by collect, not here. BlockingCaptureLines lives
// beside BlockingNotice so the two formulations of the same four facts cannot
// drift apart, which is the one requirement of the spec that came with an
// argument attached.
func blockingBlock(s State, width int) []string {
	lines := collect.BlockingCaptureLines(s.Verify.Blocking)
	if len(lines) == 0 {
		return []string{row(pad+"Blocked process capture", statusNotChecked, width)}
	}
	out := []string{row(pad+"Blocked process capture", lines[0], width)}
	for _, l := range lines[1:] {
		out = append(out, screen.Wrap(l, width, fieldPad)...)
	}
	// The wait figures appear ONLY when something has actually waited on a
	// lock. On an instance restarted last night the counters are back to zero,
	// and printing "0.0 h across 0 waits" there would read as a measurement of a
	// quiet instance rather than as the absence of any measurement. The sentence
	// is BlockingNotice's own, reused rather than rewritten.
	if s.Verify.Blocking.HasLockWaits() {
		if notice := collect.BlockingNotice(s.Verify.Blocking); len(notice) > 0 {
			out = append(out, screen.Wrap(notice[0], width, fieldPad)...)
		}
	}
	return out
}

// ---------------------------------------------------------------- screen 3

// option is one checkbox of screen 3: its label, the consequence of ticking it,
// and whether it is the indented dependent one.
//
// Every option carries its consequence beside its box rather than in a README
// nobody opens before running the tool. Six of the seven widen what the archive
// DISCLOSES — that is a decision somebody may have to justify to a security
// officer — and the seventh only costs time. Presenting them as one category
// would misprice the first six.
type option struct {
	flag, label, desc string
	dependent         bool
}

func options(s State) []option {
	// Nothing to read yet is a claim about THIS instance, so it is only made
	// when the probe found no capture. On an instance where the capture is
	// running it would be false, and this tool does not say false things about
	// what it collects.
	blocked := ".xml reports — nothing to read yet, see step 2"
	if s.Verify.Blocking.CanCapture() {
		blocked = ".xml reports — a capture is running, see step 2"
	}
	return []option{
		{collect.FlagIncludeSessionText, "session text",
			"live SQL of the 5 longest transactions, with login, host and program names", false},
		{collect.FlagObjectDefinitions, "object definitions",
			"source of views, procedures, functions — can name linked servers, can embed credentials", false},
		{collect.FlagDeadlockGraphs, "deadlock graphs",
			".xdl reports, verbatim SQL of both victims", false},
		{collect.FlagBlockedProcessReports, "blocked processes", blocked, false},
		{collect.FlagQueryStoreDetail, "query store detail",
			"untruncated query text and .sqlplan execution plans, per database", false},
		{collect.FlagQueryStorePlanStats, "plan stats",
			"also reads the whole instance plan cache to find the last profiled plan", true},
		{collect.FlagEstimateCompression, "compression estimate",
			"samples real data into tempdb; slow on large tables", false},
	}
}

func renderOptions(s State, width int) []string {
	out := []string{row(pad+"What to collect", "step 3/4", width), ""}

	// The count comes from the resolved plan and from nowhere else. The corpus
	// holds 55 files, 8 of them behind a flag, and the version gates close more
	// on each instance: a screen announcing 55 would be contradicted by the
	// gauge on the next screen.
	out = append(out, screen.Wrap(fmt.Sprintf("Always collected: %d collectors on this instance, computed from "+
		"the resolved plan. No statement text, no source code, no execution plans. Query Store configuration, "+
		"top queries and forced plans are included.", s.Verify.Collectors), width, pad)...)
	out = append(out, "")
	out = append(out, screen.Wrap("Additional. The first six widen what the archive discloses; "+
		"the last one only costs time.", width, pad)...)
	out = append(out, "")

	for i, o := range options(s) {
		out = append(out, optionLines(s, i, o, width)...)
	}
	out = append(out, "")

	// Displayed, never edited: .env stays the place where settings live, and
	// the window is what bounds everything the Query Store options above export.
	if s.QueryStoreWindow != "" {
		out = append(out, pad+"Query Store window: "+s.QueryStoreWindow)
		out = append(out, "")
	}

	if s.Collision != "" {
		// A banner above the keys, not a screen of its own. The checkboxes stay
		// reachable, because rerunning the same day TO TICK ONE is what puts a
		// collision on the screen in the first place.
		//
		// Nothing is deleted without a keystroke that names the choice: without
		// --keep, the run removes both the folder of the day and the archive
		// beside it, which may be the one just mailed.
		out = append(out, pad+"A run of the same day already exists:", fieldPad+"  "+s.Collision)
		out = append(out, screen.Wrap("[enter] replaces it; [k] writes this run beside it.", width, pad)...)
		out = append(out, "")
	}
	if !s.canStart() {
		out = append(out, screen.Wrap("Nothing to collect: "+nothingReason(s), width, pad)...)
		out = append(out, "")
		return append(out, pad+"[b] back   [q] quit")
	}
	start := "[enter] start collection"
	if s.Collision != "" {
		start = "[enter] replace it   [k] keep both"
	}
	return append(out, pad+"[tab] next   [space] toggle   "+start+"   [b] back   [q] quit")
}

// nothingReason says which of the two empty plans this is. They are not the
// same problem and they are not fixed in the same place: one is a DB_INCLUDE
// that matched nothing, the other is an instance too old for every collector
// the corpus holds.
func nothingReason(s State) string {
	if !s.Verify.Probed {
		return "the instance was not probed"
	}
	if len(s.Verify.Folders) == 0 {
		return "no database selected"
	}
	return "no collector runs on this version"
}

// optionLines draws one checkbox with its consequence folded under it. The
// description column is fixed so that the seven consequences line up, and the
// dependent option is indented under the one it needs — it does nothing on its
// own, and a checkbox that looked independent would promise a collection that
// will not happen.
func optionLines(s State, index int, o option, width int) []string {
	const descCol = 29
	lead := "   "
	if o.dependent {
		lead = "       "
	}
	if index == s.FlagIndex {
		// The selection marker replaces the last two columns of the lead, so
		// the boxes stay in line whichever row has the cursor.
		lead = lead[:len(lead)-2] + "> "
	}
	box := "[ ] "
	if s.Flags[o.flag] {
		box = "[x] "
	}
	return hang(lead+box+o.label, descCol, o.desc, width)
}

// hang lays a folded paragraph out beside a label: the first line sits in the
// column the label leaves free, and every following line is indented to that
// same column, so a consequence that needs two lines still reads as belonging
// to its label.
//
// The paragraph goes through Wrap rather than Truncate. These are consequence
// texts — what a checkbox discloses, which databases the login cannot read —
// and every word of them has to survive.
func hang(head string, col int, body string, width int) []string {
	folded := screen.Wrap(body, width, strings.Repeat(" ", col))
	if len(folded) == 0 {
		return []string{head}
	}
	gap := col - utf8.RuneCountInString(head)
	if gap < 1 {
		// A label wider than its column pushes the text one space along rather
		// than being cut: the label is a name, and a cut name is another name.
		gap = 1
	}
	first := head + strings.Repeat(" ", gap) + strings.TrimLeft(folded[0], " ")
	return append([]string{first}, folded[1:]...)
}

// ---------------------------------------------------------------- screen 4

// barWidth is how many columns the gauge gets, and it shrinks with the
// terminal rather than overflowing it: the counters to its right are what the
// operator reads, and a bar that pushed them off the line would cost more than
// it shows.
func barWidth(width int) int {
	const wanted = 30
	if width <= 0 {
		return 1
	}
	if w := width - 22; w < wanted {
		if w < 1 {
			return 1
		}
		return w
	}
	return wanted
}

// bar draws the gauge and returns it with the percentage it represents. One
// signature, and total <= 0 is guarded on the first line: Go panics on an
// integer division by zero — there is no NaN to look for here — and a gauge is
// asked to draw itself before Planned has arrived on every single run.
//
// The denominator is a number of UNITS, never a number of scripts. The corpus
// holds 55 files and a run over twelve databases is 223 units; a gauge wired to
// len(scripts) would be wrong by a factor of four and would sit at 100% while
// the collection kept going for three more minutes.
func bar(done, total, width int) (string, int) {
	if width < 0 {
		width = 0
	}
	if total <= 0 {
		return "[" + strings.Repeat("-", width) + "]", 0
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		// A gauge that reads 104% is a bug report about the wrong thing.
		done = total
	}
	filled := width * done / total
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]", done * 100 / total
}

// gaugeLine is the gauge with its counters, shared by the collection screen and
// the final one: they are the same screen, and the final one differs only in
// what is under the gauge.
func gaugeLine(s State, width int, tail string) string {
	g, pct := bar(s.DoneUnits, s.Units, barWidth(width))
	line := fmt.Sprintf("%s%s %3d%%  %d/%d", pad, g, pct, s.DoneUnits, s.Units)
	if tail != "" {
		line += "   " + tail
	}
	return line
}

func renderCollecting(s State, width int) []string {
	out := []string{
		row(pad+"Collecting", "step 4/4", width),
		"",
		gaugeLine(s, width, ""),
		"",
	}
	// The collector, the database and the elapsed time, TRUNCATED rather than
	// wrapped: they are columns, and this pair of lines is what absorbs the gap
	// between a percentage of units and a percentage of time. A Query Store
	// extraction can hold the bar at 65% for two minutes, and these two lines
	// are how the operator sees WHAT is holding it.
	out = append(out, pad+screen.Truncate(s.Script, textWidth(width)))
	out = append(out, row(pad+screen.Truncate(s.Database, textWidth(width)/2), "elapsed "+shortDuration(s.Elapsed), width))
	out = append(out, "")
	// A cumulative summary, not a scrolling log: the full log is the manifest's
	// job and it already does it.
	for _, n := range s.Notes {
		out = append(out, pad+screen.Truncate(n, textWidth(width)))
	}
	// Bytes, and deliberately not a count of files. UnitDone carries bytes; a
	// unit writes one file per result set, so a file count would mean
	// propagating a counter out of six writers — the diffuse change to collect
	// this whole batch refused to make. Bytes are also the more useful number:
	// they announce the weight of the archive to send.
	out = append(out, pad+status("written")+humanBytes(s.Bytes)+" so far", "")
	if s.Stopping {
		// Ctrl-C has been pressed and Run has not come back yet. Saying the
		// collection is over before its manifest and its archive are written
		// would end with a partial zip presented as a finished one.
		return append(out, pad+"stopping...")
	}
	return append(out, pad+"[ctrl-c] stop")
}

// textWidth is the room a full-width line has left after the two-column
// indent, never negative.
func textWidth(width int) int {
	w := width - len(pad)
	if w < 1 {
		return 1
	}
	return w
}

// renderDone is the same screen turned into a summary. There is no fifth step:
// the operator ends where the work happened.
func renderDone(s State, width int) []string {
	out := []string{
		row(pad+"Done", "step 4/4", width),
		"",
		gaugeLine(s, width, shortDuration(s.Total)),
		"",
	}
	// A cancelled run still produces its archive, and it says so. A run stopped
	// after three minutes is still worth sending, and the DBA must not discover
	// its partiality at the other end.
	if s.Cancelled {
		out = append(out, pad+"Collection stopped. This archive is partial:")
	} else {
		// The recipient is not named: this repository is public, and "whoever
		// requested the audit" is the only correct wording.
		out = append(out, pad+"Send this file to whoever requested the audit:")
	}
	out = append(out, "")
	if s.ZipPath == "" {
		// Nothing to select, so say why rather than print an empty indent.
		out = append(out, fieldPad+"  no archive was produced")
	} else {
		// The path ALONE on its line, indented: this is what a DBA drags with
		// the mouse to paste into a mail, and anything else on the line comes
		// with it. There is no checksum anywhere on this screen — it cost a
		// second read of the whole archive and either a second file to send or
		// a string that dies with the terminal.
		out = append(out, fieldPad+"  "+s.ZipPath)
		out = append(out, "", fieldPad+"  "+humanBytes(s.ZipBytes))
	}
	out = append(out, "", pad+summaryLine(s))
	if deniedPermissions(s) > 0 {
		// The reminder saves the round trip where the archive arrives and the
		// missing permissions have to be asked for again.
		out = append(out, pad+"Denied permissions are recorded in the archive.")
	}
	return append(out, "", pad+"[enter] quit")
}

func summaryLine(s State) string {
	return fmt.Sprintf("%s, %s, %s, %s",
		plural(s.DoneUnits, "collected", "collected"),
		plural(s.SkippedCount, "skipped", "skipped"),
		plural(s.ErrorCount, "error", "errors"),
		plural(deniedPermissions(s), "permission denied", "permissions denied"))
}

// deniedPermissions counts the capabilities the PREFLIGHT refused, which is
// what the sentence on the final screen says and what the line under it is
// about. It is not the number of units that failed: a unit can fail for ten
// reasons that are not a refused permission, and the two numbers answer two
// different questions.
func deniedPermissions(s State) int {
	n := 0
	for _, c := range s.Verify.Checks {
		if c.Status != "ok" {
			n++
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// shortDuration writes an elapsed time the way the mock-ups do — 2m04s, 4m38s —
// which is not what time.Duration.String gives (2m4.003s). Seconds are padded
// so the value does not jitter under a ticker refreshing it every second.
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	h, m, sec := total/3600, (total/60)%60, total%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

// humanBytes is the same spelling collect's manifest uses, kept here because
// the collect one is unexported and this batch does not widen collect's API for
// a format string. If the two ever disagree the archive and the screen would
// report different sizes for the same file, so the shape is deliberately
// identical.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d bytes", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
