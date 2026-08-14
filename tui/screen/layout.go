package screen

import (
	"strings"
	"unicode/utf8"
)

// The two characters the collect texts carry that a non-UTF-8 Windows console
// cannot draw, and their ASCII spellings. Nothing else is substituted: the
// screens use words — ok, denied, skipped, missing, not checked — and no
// symbols, so this list is the whole of the transformation. Accented letters
// are deliberately absent, because rewriting COMPTABILITÉ to COMPTABILITE would
// rename a database on the screen the DBA reads to identify it.
var asciiFolds = strings.NewReplacer(
	"—", "-",
	"…", "...",
)

// ToASCII spells the frame with ASCII only, for a console whose output code
// page could not be set to UTF-8.
func ToASCII(s string) string { return asciiFolds.Replace(s) }

// ellipsis marks a value that was cut. One rune, so it costs one column.
const ellipsis = "…"

// Truncate fits a column value into width columns, counting runes rather than
// bytes so an accented database name is not cut in the middle of a character.
//
// This is the regime for columns only — collector name, database, counters —
// where a value that overflows would push every following column out of line.
// It must never be used on an Impact: cutting "the report must not read this as
// 'no log shipping'" after "no lo…" reverses its meaning. Those go through Wrap.
func Truncate(s string, width int) string {
	if width <= 0 {
		// GetSize reports 0x0 while an RDP window is being dragged, and the
		// renderer keeps drawing through it.
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	// Spend the last column on the marker: a value silently cut reads as a
	// shorter value, which on a database name is a different name.
	var b strings.Builder
	kept := 0
	for _, r := range s {
		if kept == width-1 {
			break
		}
		b.WriteRune(r)
		kept++
	}
	return b.String() + ellipsis
}

// Wrap folds a paragraph onto lines at most width columns wide, each carrying
// indent. It is the regime for consequence texts — the Impact of a denied
// permission, the blocked process capture sentences — where every word has to
// survive.
//
// A word wider than the space available overflows its own line rather than
// being cut. That is the deliberate trade: an over-wide line is ugly on a
// narrow terminal, a cut word can be wrong, and only one of those two costs a
// return visit to the client.
func Wrap(s string, width int, indent string) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	// At least one column of text, whatever the caller was told by GetSize.
	// Without this floor a zero width would put every word on its own line at
	// best, and loop forever at worst.
	avail := width - utf8.RuneCountInString(indent)
	if avail < 1 {
		avail = 1
	}

	var lines []string
	cur := words[0]
	curLen := utf8.RuneCountInString(cur)
	for _, w := range words[1:] {
		n := utf8.RuneCountInString(w)
		if curLen+1+n > avail {
			lines = append(lines, indent+cur)
			cur, curLen = w, n
			continue
		}
		cur += " " + w
		curLen += 1 + n
	}
	return append(lines, indent+cur)
}

// DecodeKey turns the head of b into one keystroke and reports how many bytes
// it consumed, or 0 when b does not yet hold a complete keystroke and the
// caller must read more. It lives here rather than in terminal.go so it can be
// tested without a terminal, which is the only way it gets tested at all.
//
// Two failures of the obvious one-byte decoder are what this function is for.
// Returning rune(b[0]) splits a password containing é into two characters, so
// the login fails with a password that was typed correctly; and consuming only
// the escape of an up arrow leaves "[A" in the buffer, which lands in the
// server field of a DBA who thought they were recalling the previous value.
func DecodeKey(b []byte) (Key, int) {
	if len(b) == 0 {
		return Key{}, 0
	}
	switch b[0] {
	case '\r':
		// CRLF is one keystroke, not two. A terminal that sends both, or a
		// pasted line ending, otherwise delivered two [enter]s: on screen 2 the
		// first advances to screen 3 and the second reaches canStart() and
		// launches the collection — and with a same-day collision on screen,
		// that second [enter] also means "replace it", destroying the previous
		// run's folder and zip without anyone having read the question.
		//
		// The pair is only recognised inside one buffer. Split across two
		// reads, the \n arrives on its own and is an [enter] again; ReadKey
		// keeps a partial rune between calls but not a partial pair, and a
		// keystroke that waits for the next byte to find out what it is would
		// make every real [enter] hang until the one after it.
		if len(b) > 1 && b[1] == '\n' {
			return Key{Named: KeyEnter}, 2
		}
		return Key{Named: KeyEnter}, 1
	case '\n':
		return Key{Named: KeyEnter}, 1
	case '\t':
		return Key{Named: KeyTab}, 1
	case ' ':
		return Key{Named: KeySpace}, 1
	case 0x03:
		// Raw mode delivers Ctrl-C as a byte rather than as SIGINT. This is the
		// only place the wizard can ever see it, which is why its main
		// goroutine must never sit inside the collection.
		return Key{Named: KeyCtrlC}, 1
	case 0x7f, 0x08:
		// Backspace is 0x7f on a terminal and 0x08 on a Windows console.
		return Key{Named: KeyBackspace}, 1
	case 0x1b:
		return decodeEscape(b)
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size <= 1 {
		if utf8.FullRune(b) || len(b) >= utf8.UTFMax {
			// A byte that cannot start a rune, or a lead byte whose
			// continuation never came. Drop it rather than emit U+FFFD: a
			// replacement character silently inserted into a password field is
			// a login failure nobody can explain.
			return Key{}, 1
		}
		return Key{}, 0 // the rest of the rune is still in flight
	}
	return Key{Rune: r}, size
}

// escapeMax bounds how long an unterminated escape sequence may keep the reader
// waiting. Nothing a keyboard sends is anywhere near this long; the bound only
// exists so a stray 0x1b from a mangled paste cannot stall input forever.
const escapeMax = 16

// decodeEscape consumes a whole CSI or SS3 sequence and reports KeyNone: the
// wizard binds letters, enter, tab and space, so arrows and function keys are
// recognised only in order to be swallowed. There is no Esc key to report —
// see the NamedKey list.
func decodeEscape(b []byte) (Key, int) {
	// The bound is tested once, ahead of every shape, so no branch below can
	// forget it: whatever the bytes look like, an escape that has not resolved
	// in escapeMax of them gives up its first byte and lets the rest be decoded
	// on their own merits. A branch answering "not yet" indefinitely would hold
	// ReadKey inside Read until the operator happened to press another key.
	if len(b) >= escapeMax {
		return Key{}, 1
	}
	if len(b) == 1 {
		// Ambiguous: a lone Esc looks exactly like the first byte of an arrow.
		// Waiting is the safe half of that choice, because Esc is bound to
		// nothing — the screens offer [q], [b] and [ctrl-c] — while a leaked
		// "[A" is typed into a field.
		return Key{}, 0
	}
	switch b[1] {
	case '[': // CSI: ESC [ parameters... final byte in 0x40..0x7e
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				return Key{}, i + 1
			}
		}
		return Key{}, 0
	case 'O': // SS3: ESC O x, the application-mode arrows and F1-F4
		if len(b) < 3 {
			return Key{}, 0
		}
		return Key{}, 3
	}
	// ESC followed by anything else is Alt+key on most terminals. The wizard
	// has no Alt bindings, so the escape is dropped and the key that follows is
	// decoded on the next call, on its own merits.
	return Key{}, 1
}
