// Package tui is the four-step wizard: a pure state machine, a pure renderer,
// and the wiring that connects them to collect and to a terminal.
//
// The split matters more than it looks. Everything a test could get wrong —
// which key does what, which screen refuses to continue, what a screen says
// when the probe failed — lives in State and Render, neither of which touches
// a terminal, a connection or a clock. What is left in run.go is the part no
// test in this repository can exercise, and it is kept as thin as that makes
// possible.
package tui

import (
	"time"
	"unicode/utf8"

	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

// Step is where the operator is in the wizard. The four screens the spec
// describes are six steps here, because two of the four are preceded by a wait
// that has its own screen: connecting to the instance and probing it both take
// as long as the server makes them take, and a wizard that painted the
// destination screen while the connection was still being made would be the
// frozen screen this whole package exists to remove.
type Step int

const (
	StepConnection Step = iota
	// StepConnecting is a wait: an activity indicator, and cancellable. Open
	// and the first Conn spend up to SQL_CONNECT_TIMEOUT_SEC each.
	StepConnecting
	StepVerification
	// StepVerifying is the same, and the longer of the two: Verify runs a
	// preflight of eight capabilities, a server probe, the database list and
	// the blocking probe, each bounded by SQL_QUERY_TIMEOUT_SEC. On a frozen
	// instance that is minutes.
	StepVerifying
	StepOptions
	StepCollecting
	StepDone
	StepQuit
)

// The two editable fields of screen 1, in tab order. They are the only two:
// the database, the authentication mode and the encryption settings come from
// the resolved configuration and the wizard does not edit .env.
const (
	fieldServer = iota
	fieldPassword
	fieldCount
)

// flagOrder is the seven opt-ins in the order screen 3 lists them, which is
// the order the spec's mock-up fixes: the six that widen what the archive
// discloses first, with plan stats indented under its prerequisite, and the
// one that only costs time last.
//
// It is built from the constants rather than from collect.KnownFlags, and not
// only because a map has no order: KnownFlags is a flag-to-CLI-spelling map
// used to write "--object-definitions" into a message, and nothing on these
// screens has a command line to name.
var flagOrder = []string{
	collect.FlagIncludeSessionText,
	collect.FlagObjectDefinitions,
	collect.FlagDeadlockGraphs,
	collect.FlagBlockedProcessReports,
	collect.FlagQueryStoreDetail,
	collect.FlagQueryStorePlanStats,
	collect.FlagEstimateCompression,
}

// State is the whole wizard. It is a value: every transition returns a new one
// and nothing in this package holds a pointer to it, which is what lets the
// three producers — keyboard, ticker, observer — be serialised through a
// single channel with no mutex anywhere.
type State struct {
	Step Step

	// Version and Build are stamped into every screen's header. Not
	// decoration: a screenshot of a wizard is the terminal transcript a bug
	// report will carry, and check and collect print the same pair so an
	// archive, a transcript and a report can be tied to one build.
	Version, Build string

	// Screen 1. Password is held here and nowhere else: it is never written
	// to disk, never rendered, and never leaves the process.
	Server, Password string
	// ServerFromFlag records where the initial address came from, and exists
	// only so ServerOverridden can be set on the first edit. A value out of
	// .env or of the environment is not worth mentioning; one typed on the
	// command line half a minute ago is.
	ServerFromFlag bool
	// ServerOverridden records that the operator typed over a value that came
	// from a --server flag. The screen says so, because somebody who typed a
	// command line and then edited the field must not be surprised by where
	// the connection went. The typing wins: it is the last thing the operator
	// did, and a flag quietly beating it would send the connection somewhere
	// other than what the screen displays.
	ServerOverridden bool
	Field            int
	// ConnError is the refusal from screen 1, which is the one error in this
	// wizard that does not end it: the operator stays on the screen, with the
	// cursor back in the server field.
	ConnError error

	// Screen 2.
	Verify     collect.VerifyResult
	GrantPath  string
	GrantError error

	// Screen 3.
	Flags     map[string]bool
	FlagIndex int
	// Collision is the name an earlier run of the same day already occupies,
	// or "" when nothing is in the way. Non-empty turns the bottom of screen 3
	// into a prompt, because without --keep the run is about to delete both
	// that folder and the archive beside it.
	Collision string
	Keep      bool

	// ASCII is set when the console code page could not be moved to UTF-8, in
	// which case every rendered line goes through screen.ToASCII. It is on the
	// state rather than read from the terminal at paint time so the renderer
	// stays a pure function of this struct.
	ASCII bool

	// Screen 4, fed by the observer adapter.
	Units, DoneUnits, Databases int
	Script, Database            string
	Elapsed                     time.Duration
	// Spinner is the activity indicator's phase, advanced by the ticker. It
	// exists for the two waits as much as for the collection: something has to
	// move while the wizard waits on a server that may never answer.
	Spinner int

	DeniedCount, SkippedCount, ErrorCount int
	Bytes                                 int64
	// Notes is the cumulative summary under the gauge — denials, skips — not
	// a scrolling log. The full log is the manifest's job and it already does
	// it.
	Notes []string

	// The final screen.
	ZipPath  string
	ZipBytes int64
	Total    time.Duration
	// Stopping is set the moment Ctrl-C is pressed during the collection and
	// stays set: collect.Run keeps going until it has written its manifest and
	// its archive, and the screen has to say what it is doing meanwhile.
	Stopping bool
	// Cancelled is set once that run has come back, and it is what makes the
	// final screen say the archive is partial rather than complete.
	Cancelled bool

	// QueryStoreWindow is the resolved collection window, shown on screen 3
	// because the flags there change what the Query Store collectors export
	// and the window is what bounds it. It is displayed, never edited: .env
	// remains the place where settings live.
	QueryStoreWindow string
}

// Key applies one keystroke and returns the resulting state. It is the whole
// of the wizard's behaviour and it is pure: no clock, no terminal, no
// connection. Anything that takes time is expressed as a move into a waiting
// step, which run.go notices and services.
func (s State) Key(k screen.Key) State {
	switch s.Step {
	case StepConnection:
		return s.keyConnection(k)
	case StepConnecting, StepVerifying:
		// A wait offers exactly one choice: give up on it. Ctrl-C cancels the
		// context and drops back to screen 1, where the operator can fix the
		// address that is not answering. Esc is the same key for anybody who
		// reads Ctrl-C as "kill the program".
		if k.Named == screen.KeyCtrlC || k.Named == screen.KeyEsc {
			s.Step = StepConnection
			s.Field = fieldServer
		}
		return s
	case StepVerification:
		return s.keyVerification(k)
	case StepOptions:
		return s.keyOptions(k)
	case StepCollecting:
		if k.Named == screen.KeyCtrlC {
			// The step does not change. The run is still writing, and telling
			// the operator it is over before it is would be a lie that ends
			// with an incomplete zip presented as a finished one.
			s.Stopping = true
		}
		return s
	case StepDone:
		if k.Named == screen.KeyEnter || k.Rune == 'q' {
			s.Step = StepQuit
		}
		return s
	}
	return s
}

// keyConnection handles screen 1, the only editable screen and the only
// capability the wizard adds over the command line.
//
// It exists for the password. The repository deliberately refuses a
// --password flag, since it would end up in ps and in the shell history, so a
// DBA who was handed a binary and no .env has no way to supply one at all —
// and that operator is exactly who this wizard is for. The value is held in
// memory, shown as stars, and never written to disk.
//
// This is also why the screen quits on Esc rather than on [q]: every printable
// rune belongs to the field being edited, and a server named QUALIF or a
// password containing a q must not close the program.
func (s State) keyConnection(k screen.Key) State {
	switch k.Named {
	case screen.KeyEnter:
		// The previous refusal disappears the moment a new attempt starts, so
		// the screen never shows an error belonging to an older address.
		s.ConnError = nil
		s.Step = StepConnecting
		return s
	case screen.KeyCtrlC, screen.KeyEsc:
		s.Step = StepQuit
		return s
	case screen.KeyTab:
		// Moving the cursor is not an edit, so it does not claim an override.
		s.Field = (s.Field + 1) % fieldCount
		return s
	case screen.KeyBackspace:
		return s.editField(func(v string) string {
			// By rune, not by byte. A password of "clé" is four bytes and
			// three runes; dropping the last byte leaves invalid UTF-8 in the
			// connection string and a refusal nothing on screen explains,
			// since the field shows only stars.
			if v == "" {
				return v
			}
			_, size := utf8.DecodeLastRuneInString(v)
			return v[:len(v)-size]
		})
	case screen.KeySpace:
		return s.editField(func(v string) string { return v + " " })
	}
	if k.Rune >= 0x20 && k.Rune != 0x7f {
		return s.editField(func(v string) string { return v + string(k.Rune) })
	}
	return s
}

// editField applies one edit to whichever of the two fields has the cursor,
// and records the override on the way through so the three call sites above
// cannot each forget it.
func (s State) editField(edit func(string) string) State {
	if s.Field == fieldPassword {
		s.Password = edit(s.Password)
		return s
	}
	if s.ServerFromFlag {
		s.ServerOverridden = true
	}
	s.Server = edit(s.Server)
	return s
}

// connectFailed is the one error in this wizard that does not end a step. A
// refused connection reprints the server's own message and hands the keyboard
// back to the server field, because the address is both the likeliest thing to
// be wrong and the one a wrong password does not explain.
//
// It is a method rather than an event type so that the decision — which field,
// which step — is testable without a channel, a goroutine or a driver.
func (s State) connectFailed(err error) State {
	s.Step = StepConnection
	s.ConnError = err
	s.Field = fieldServer
	return s
}

// keyVerification handles screen 2.
//
// [g] is absent from this switch on purpose: it writes a file, and Key is
// pure. The loop calls writeGrantScript for it, which is where the file name,
// the directory and the failure all live — and where they can be tested
// without a keyboard.
func (s State) keyVerification(k screen.Key) State {
	switch {
	case k.Rune == 'r':
		// Back into the wait, not into a second copy of this screen: the
		// probe is the slow part, and its own step is the one that shows an
		// activity indicator and takes Ctrl-C.
		s.Step = StepVerifying
	case k.Rune == 'b':
		s.Step = StepConnection
		s.Field = fieldServer
	case k.Named == screen.KeyEnter:
		// A probe that did not succeed disables the continuation, and this is
		// where that is enforced rather than only drawn. Starting a collection
		// whose coverage is unknown is exactly what the verification screen
		// exists to prevent, and the gauge's denominator would be wrong too.
		if s.canContinue() {
			s.Step = StepOptions
		}
	case k.Rune == 'q', k.Named == screen.KeyCtrlC:
		s.Step = StepQuit
	}
	return s
}

// keyOptions handles screen 3, including the same-day collision prompt, which
// takes over the keyboard while it is up.
func (s State) keyOptions(k screen.Key) State {
	if s.Collision != "" {
		// Nothing is destroyed without a keystroke that names the choice. Any
		// key that is not one of the three does nothing at all — in
		// particular, no key "defaults" to replacing, because the thing being
		// replaced may be the archive the operator has just mailed.
		switch {
		case k.Named == screen.KeyEnter:
			s.Keep = false
			s.Collision = ""
			s.Step = StepCollecting
		case k.Rune == 'k':
			s.Keep = true
			s.Collision = ""
			s.Step = StepCollecting
		case k.Rune == 'b':
			s.Step = StepVerification
		}
		return s
	}
	switch {
	case k.Named == screen.KeySpace:
		if s.FlagIndex >= 0 && s.FlagIndex < len(flagOrder) {
			s.Flags = toggleFlag(s.Flags, flagOrder[s.FlagIndex])
		}
	case k.Named == screen.KeyTab:
		// The arrow keys are decoded to KeyNone on purpose — letting a CSI
		// sequence reach a text field would type "[A" into a server name — so
		// tab is what moves the selection, as it does on screen 1.
		s.FlagIndex = (s.FlagIndex + 1) % len(flagOrder)
	case k.Named == screen.KeyEnter:
		if s.canStart() {
			s.Step = StepCollecting
		}
	case k.Rune == 'b':
		s.Step = StepVerification
	case k.Rune == 'q', k.Named == screen.KeyCtrlC:
		s.Step = StepQuit
	}
	return s
}

// canContinue reports whether screen 2 may lead anywhere. False on a probe
// that failed, which is not the same as a connection that failed: the wizard
// stays on screen 2 in that case, since a denied permission on a perfectly
// good connection would otherwise send the operator back to correct a server
// name that was right.
func (s State) canContinue() bool { return s.Verify.Probed }

// canStart reports whether there is anything to collect. Zero collectors means
// a resolved plan in which nothing runs — every version gate closed on a 2012
// instance, or a corpus that could not be read — and an archive containing
// only its manifest is a round trip the wizard exists to save.
func (s State) canStart() bool { return s.Verify.Probed && s.Verify.Collectors > 0 }

// writeGrantScript is the impure half of [g], kept out of Key so that the
// state machine remains a pure function of keystrokes and the file writing
// remains testable without one.
//
// It never leaves screen 2, in either outcome. On success the operator has a
// path to hand to whoever grants permissions; on failure they have the reason
// in full, and they can still continue — a missing grant script does not stop
// a degraded collection, which the repository counts as a success.
func (s State) writeGrantScript(outputDir, tool string, now time.Time) State {
	if s.Step != StepVerification {
		return s
	}
	path, err := writeGrants(s.Verify, outputDir, tool, now)
	s.GrantPath, s.GrantError = path, err
	if err != nil {
		s.GrantPath = ""
	}
	return s
}

// collisionFor names what an earlier run of the same day already occupies, or
// "" when the way is clear. It is the whole of the wizard's guard against the
// destructive half of prepareRunFolder, and it runs BEFORE collect.Run:
// afterwards the folder and the archive are already gone.
//
// keep is passed through rather than assumed false because RunFolderFor's
// answer depends on it — under --keep it suffixes until it finds a free name,
// so there is nothing to warn about — and asking the question about a path the
// run will not use would produce a prompt about a file nothing was going to
// touch.
func collisionFor(outputDir, instance string, now time.Time, keep bool) string {
	folder := collect.RunFolderFor(outputDir, instance, now, keep)
	if taken, what := collect.RunNameTaken(folder); taken {
		return what
	}
	return ""
}

// toggleFlag flips one opt-in and enforces the one dependency between them. It
// returns a new map: states are values here, and a shared map would let a
// keystroke reach backwards into every state already handed out.
//
// The dependency runs both ways because it is the same fact stated twice.
// query_store_plan_stats searches the instance-wide plan cache for the last
// profiled plan OF EACH EXTRACTED QUERY, so with nothing extracting queries it
// has nothing to look up: it cannot be turned on alone, and it goes off with
// the extraction it depended on. A checkbox left ticked under an unticked
// prerequisite would claim a collection that will not happen.
func toggleFlag(flags map[string]bool, name string) map[string]bool {
	out := make(map[string]bool, len(flags)+1)
	for k, v := range flags {
		out[k] = v
	}
	if name == collect.FlagQueryStorePlanStats && !out[collect.FlagQueryStoreDetail] {
		return out
	}
	out[name] = !out[name]
	if name == collect.FlagQueryStoreDetail && !out[name] {
		out[collect.FlagQueryStorePlanStats] = false
	}
	return out
}
