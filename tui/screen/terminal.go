// Package screen is the only part of the wizard that touches a real terminal.
// Everything above it deals in []string frames and Key values, which is what
// makes the state machine and the renderer testable without allocating a
// pseudo-terminal — something no test in this repository can do portably.
package screen

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// NamedKey is a keystroke that carries no character: the wizard reacts to it by
// name rather than by content. Everything else arrives as a rune in Key.Rune,
// which is what the server and password fields consume.
type NamedKey int

const (
	// KeyNone means "nothing actionable was read". A decoder that recognised a
	// sequence it has no use for — an arrow, a function key — returns this
	// rather than leaking its bytes into a text field.
	KeyNone NamedKey = iota
	KeyEnter
	KeyTab
	KeySpace
	KeyCtrlC
	// There is no KeyEsc. A lone 0x1b cannot be told apart from the first byte
	// of an arrow sequence without waiting for the next keystroke, so a screen
	// that offered [esc] would need two presses to answer one; and since no
	// arrow is bound to anything, nothing needs the key. Escape sequences are
	// consumed and reported as KeyNone.
	KeyBackspace
)

// Key is one keystroke. Named is KeyNone for a printable character, and Rune is
// then the character; for every other Named value Rune is 0. The two are kept
// in one struct so a caller handles a keystroke with a single switch instead of
// juggling two channels of input.
type Key struct {
	Named NamedKey
	Rune  rune
}

// Terminal owns the raw mode, the console code page and the frame buffer for
// the duration of the wizard. There is exactly one per process and its Close
// must run on every exit path, panic included, or the operator is left with a
// shell that echoes nothing.
type Terminal struct {
	in, out *os.File

	// prior is the cooked-mode state captured by MakeRaw. Nil once restored,
	// which is what makes Close safe to call twice — the deferred Close in
	// tui.Run and an explicit one on the quit path both reach it.
	prior *term.State
	// restoreConsole undoes the Windows-only console tweaks. Never nil.
	restoreConsole func()

	ascii bool

	// pending holds bytes read from the keyboard that did not yet form a
	// complete keystroke: half of a two-byte rune, or an escape sequence
	// arriving split across two reads. Dropping them would corrupt a password
	// containing an accented character, so they survive between ReadKey calls.
	pending []byte
	// scratch is the read buffer. 64 bytes is far more than a keystroke needs;
	// it exists so a paste — which arrives as one burst — is consumed in a few
	// reads instead of one syscall per byte.
	scratch [64]byte
}

// Open puts the terminal in raw mode and prepares its output. It fails rather
// than degrading: without raw mode the password field would echo, and a
// password left in the scrollback of a shared RDP session is a worse outcome
// than no wizard at all. The caller falls back to `sql-auditor collect`.
func Open(in, out *os.File) (*Terminal, error) {
	prior, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("cannot switch the terminal to raw mode: %w", err)
	}
	restore, ascii, err := prepareConsole(out)
	if err != nil {
		// Undo the raw mode we just took before handing the failure back: an
		// error return must leave the terminal exactly as we found it, because
		// nobody will call Close on a Terminal they never received.
		_ = term.Restore(int(in.Fd()), prior)
		return nil, err
	}
	t := &Terminal{in: in, out: out, prior: prior, restoreConsole: restore, ascii: ascii}
	// Hide the cursor for the whole session. Draw repaints the entire screen on
	// every frame, so the cursor would otherwise be parked wherever the last
	// line ended and blink there through every redraw.
	_, _ = emit(t.out, "\x1b[?25l")
	return t, nil
}

// Close restores everything Open changed, in reverse order, and is safe to call
// more than once. It returns the first failure but keeps going: a failed code
// page restore must not stop the raw mode from being lifted.
func (t *Terminal) Close() error {
	if t == nil || t.prior == nil {
		return nil
	}
	// Clear before showing the cursor again so the shell prompt comes back at
	// the top of a clean screen rather than under the last frame.
	_, firstErr := emit(t.out, "\x1b[?25h\x1b[H\x1b[2J")
	t.restoreConsole()
	if err := term.Restore(int(t.in.Fd()), t.prior); err != nil && firstErr == nil {
		firstErr = err
	}
	t.prior = nil
	return firstErr
}

// ASCIIOnly reports that the output cannot be trusted to carry UTF-8 — a
// Windows console whose code page could not be set. The renderer then runs
// every line through ToASCII. It is not a preference; it is a fact about the
// device.
func (t *Terminal) ASCIIOnly() bool { return t.ascii }

// Size is read afresh before every repaint rather than tracked, which is how
// the wizard handles a resized window without a SIGWINCH handler and without
// the Windows equivalent that does not exist. Zero is returned as zero: callers
// must survive it, because GetSize reports 0x0 while an RDP window is being
// dragged.
func (t *Terminal) Size() (width, height int) {
	w, h, err := term.GetSize(int(t.out.Fd()))
	if err != nil {
		return 0, 0
	}
	return w, h
}

// Draw repaints the whole screen. There is no partial repaint: the frames are
// at most a screenful of short lines, diffing them would cost more code than it
// saves bytes, and a full repaint is the only rendering that cannot drift out
// of sync with the state it came from.
func (t *Terminal) Draw(lines []string) error {
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	for _, l := range lines {
		// CRLF, not LF: raw mode turned off the output post-processing that
		// would otherwise supply the carriage return, so a bare \n would leave
		// each line starting where the previous one ended.
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	_, err := emit(t.out, b.String())
	return err
}

// ReadKey blocks until one complete keystroke is available. It buffers, because
// a keystroke is not a byte: an accented character is two bytes and an arrow is
// three, and either can be split across two reads on a slow link. DecodeKey
// decides where a keystroke ends; this method only feeds it bytes.
func (t *Terminal) ReadKey() (Key, error) {
	for {
		if k, n := DecodeKey(t.pending); n > 0 {
			t.pending = t.pending[n:]
			return k, nil
		}
		n, err := t.in.Read(t.scratch[:])
		if n > 0 {
			t.pending = append(t.pending, t.scratch[:n]...)
			continue
		}
		if err == nil {
			continue
		}
		// Close() closing the descriptor under us is the designed way to
		// unblock this call once the wizard has quit; the caller treats any
		// error as "stop reading" and needs no second shutdown mechanism.
		return Key{}, err
	}
}

// emit writes a string to the terminal. Wrapped so each escape sequence above
// reads as one expression, and named away from the io package.
func emit(f *os.File, s string) (int, error) { return f.WriteString(s) }
