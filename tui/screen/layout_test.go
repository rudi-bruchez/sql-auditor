package screen

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// A real Impact, taken verbatim from preflight.go. It is the text these two
// regimes exist for: it must survive wrapping word for word, and it must never
// be handed to Truncate.
const logShippingImpact = "log shipping configuration and lag not collected — the report must not read this as 'no log shipping'"

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// Truncate protects the alignment of the columns — collector name, database,
// counters — so its only hard promise is that nothing it returns is wider than
// it was asked for, whatever it is given. Width 0 is not a pathological case
// here: GetSize reports zero while an RDP window is being dragged.
func TestTruncateKeepsColumnsAligned(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"a short value is left alone", "AUDIT_RO", 20, "AUDIT_RO"},
		{"an exact fit is left alone", "AUDIT_RO", 8, "AUDIT_RO"},
		{"a long value is cut and marked", "COMPTABILITE_2019", 8, "COMPTAB…"},
		{"runes are counted, not bytes", "COMPTABILITÉ", 8, "COMPTAB…"},
		{"width zero yields nothing", "COMPTABILITÉ", 0, ""},
		{"a negative width yields nothing", "COMPTABILITÉ", -3, ""},
		{"width one still fits the marker", "COMPTABILITÉ", 1, "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Truncate(c.s, c.width)
			if got != c.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", c.s, c.width, got, c.want)
			}
			if c.width > 0 && runeLen(got) > c.width {
				t.Errorf("Truncate(%q, %d) = %q, %d runes wide", c.s, c.width, got, runeLen(got))
			}
		})
	}
}

// Wrapping is the other regime, and the whole reason there are two: cutting
// this text after "no lo…" would leave the report saying the opposite of what
// the instance is. Every word has to come out the far side.
func TestWrapKeepsTheWholeImpactText(t *testing.T) {
	const indent = "         "
	lines := Wrap(logShippingImpact, 60, indent)
	if len(lines) < 2 {
		t.Fatalf("a 105-character impact fitted on %d line(s) of 60 columns", len(lines))
	}
	for _, l := range lines {
		if runeLen(l) > 60 {
			t.Errorf("line %q is %d columns wide, want at most 60", l, runeLen(l))
		}
		if !strings.HasPrefix(l, indent) {
			t.Errorf("line %q does not carry the continuation indent", l)
		}
	}
	want := strings.Fields(logShippingImpact)
	got := strings.Fields(strings.Join(lines, " "))
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("wrapping changed the text:\n got %q\nwant %q", got, want)
	}
}

// Zero width happens, and the answer is not "drop the text". A word wider than
// the space available overflows its line rather than being cut, because losing
// characters here is exactly the failure Wrap exists to prevent.
func TestWrapSurvivesAZeroWidth(t *testing.T) {
	for _, w := range []int{0, -1, 3} {
		lines := Wrap(logShippingImpact, w, "  ")
		if len(lines) == 0 {
			t.Fatalf("Wrap(..., %d, ..) dropped the whole text", w)
		}
		got := strings.Fields(strings.Join(lines, " "))
		if strings.Join(got, " ") != strings.Join(strings.Fields(logShippingImpact), " ") {
			t.Errorf("Wrap(..., %d, ..) lost words: %q", w, got)
		}
	}
	if lines := Wrap("   ", 40, "  "); len(lines) != 0 {
		t.Errorf("Wrap of blank text produced %d line(s), want none", len(lines))
	}
}

// The substitution list is short on purpose: the screens use words — ok,
// denied, skipped, missing, not checked — and no symbols at all, so the only
// non-ASCII characters that can reach a frame are the ones the collect texts
// and the truncation marker carry.
func TestToASCIIReplacesTheEmDashesOfTheImpactTexts(t *testing.T) {
	got := ToASCII(ToASCII(logShippingImpact))
	const want = "log shipping configuration and lag not collected - the report must not read this as 'no log shipping'"
	if got != want {
		t.Errorf("ToASCII = %q, want %q", got, want)
	}
	if got := ToASCII("COMPTAB…"); got != "COMPTAB..." {
		t.Errorf("ToASCII of a truncation marker = %q", got)
	}
	if got := ToASCII("• item"); got != "- item" {
		t.Errorf("ToASCII of a bullet = %q", got)
	}
	// Accented letters are left alone: a code page that cannot show them is a
	// display problem, and replacing COMPTABILITÉ with COMPTABILITE would
	// rename a database in a screen the DBA reads to identify it.
	if got := ToASCII("COMPTABILITÉ"); got != "COMPTABILITÉ" {
		t.Errorf("ToASCII rewrote an accented name to %q", got)
	}
	for _, b := range []byte(ToASCII(logShippingImpact)) {
		if b >= 0x80 {
			t.Fatalf("ToASCII left a byte %#x in its output", b)
		}
	}
}

// A password containing é is the path this decoder exists to protect. Reading
// one byte and calling rune(b[0]) would put two characters into the field, and
// the login would fail with a password the DBA typed correctly.
func TestDecodeKeyAssemblesAMultiByteRune(t *testing.T) {
	b := []byte("é")
	if len(b) != 2 {
		t.Fatalf("test premise wrong: %q is %d bytes", b, len(b))
	}
	// Byte by byte, as a slow link delivers it: the first half is not a key.
	if k, n := DecodeKey(b[:1]); n != 0 {
		t.Errorf("half a rune decoded as %+v consuming %d bytes, want a request for more", k, n)
	}
	k, n := DecodeKey(b)
	if n != 2 || k.Named != KeyNone || k.Rune != 'é' {
		t.Errorf("DecodeKey(%q) = %+v, %d, want the single rune 'é' over 2 bytes", b, k, n)
	}
}

// The up arrow must be swallowed whole. Consuming only the escape would leave
// "[A" in the buffer, and those two characters would be typed into the server
// field by a DBA who thought they were recalling the previous value.
func TestDecodeKeyConsumesAnArrowSequence(t *testing.T) {
	k, n := DecodeKey([]byte("\x1b[A"))
	if n != 3 || k.Named != KeyNone || k.Rune != 0 {
		t.Errorf("DecodeKey(ESC [ A) = %+v, %d, want KeyNone over 3 bytes", k, n)
	}
	// Split across two reads, the sequence must still not leak: an incomplete
	// escape asks for more bytes rather than resolving to Esc.
	if _, n := DecodeKey([]byte("\x1b[")); n != 0 {
		t.Errorf("an incomplete escape sequence consumed %d bytes, want 0", n)
	}
	// And the rest of the input is untouched: the 'q' after the arrow is still
	// a quit.
	k, n = DecodeKey([]byte("\x1b[Aq"))
	if n != 3 {
		t.Fatalf("consumed %d bytes of ESC [ A q, want 3", n)
	}
	if k, n := DecodeKey([]byte("\x1b[Aq")[n:]); n != 1 || k.Rune != 'q' {
		t.Errorf("after the arrow: %+v, %d, want 'q'", k, n)
	}
}

// The named keys are the whole vocabulary of the wizard. Ctrl-C is in the list
// because raw mode delivers it as a byte and not as a signal.
func TestDecodeKeyNamesTheControlKeys(t *testing.T) {
	cases := []struct {
		in   string
		want NamedKey
	}{
		{"\r", KeyEnter},
		{"\n", KeyEnter},
		{"\t", KeyTab},
		{" ", KeySpace},
		{"\x03", KeyCtrlC},
		{"\x7f", KeyBackspace},
		{"\x08", KeyBackspace},
	}
	for _, c := range cases {
		k, n := DecodeKey([]byte(c.in))
		if n != 1 || k.Named != c.want {
			t.Errorf("DecodeKey(%q) = %+v, %d, want named %d over 1 byte", c.in, k, n, c.want)
		}
	}
	if k, n := DecodeKey(nil); n != 0 || k.Named != KeyNone {
		t.Errorf("DecodeKey(nil) = %+v, %d, want a request for more", k, n)
	}
}

// A byte that can never start a rune is dropped rather than turned into U+FFFD:
// the alternative is a replacement character silently inserted into a password.
func TestDecodeKeyDropsAnInvalidByte(t *testing.T) {
	k, n := DecodeKey([]byte{0xff, 'q'})
	if n != 1 || k.Named != KeyNone || k.Rune != 0 {
		t.Errorf("DecodeKey(0xff) = %+v, %d, want it dropped as one byte", k, n)
	}
	// A truncated sequence that can never complete must not stall the reader
	// forever either.
	if _, n := DecodeKey([]byte{0xc3, 0xc3, 0xc3, 0xc3}); n == 0 {
		t.Error("a run of invalid lead bytes asked for more input indefinitely")
	}
}

// An escape that never resolves must not hold the reader. ReadKey loops on
// Read while DecodeKey answers "not yet", so a shape that answered it for ever
// would block the wizard until the operator happened to press another key —
// with nothing on screen explaining why the last keystroke did nothing.
//
// The bound is checked once, ahead of every shape, so this holds for the CSI
// that never sends a final byte, for the SS3 whose third byte never comes, and
// for a lone escape from a mangled paste alike.
func TestDecodeKeyNeverAsksForMoreThanEscapeMaxBytes(t *testing.T) {
	for _, head := range []string{"\x1b", "\x1b[", "\x1bO", "\x1b[0;1;2;3;4;5;6"} {
		in := append([]byte(head), bytes.Repeat([]byte{'0'}, escapeMax)...)
		for len(in) >= escapeMax {
			_, n := DecodeKey(in)
			if n <= 0 {
				t.Fatalf("DecodeKey(%q) consumed nothing with %d bytes buffered", in, len(in))
			}
			in = in[n:]
		}
	}
}
