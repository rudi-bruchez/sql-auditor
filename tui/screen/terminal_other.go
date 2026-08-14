//go:build !windows

package screen

import "os"

// prepareConsole has nothing to do outside Windows. Terminals on Linux and
// macOS interpret ANSI unconditionally and their encoding is a property of the
// locale, not of a settable console attribute — there is no equivalent of
// SetConsoleOutputCP to call and nothing to restore.
//
// asciiOnly stays false here deliberately, rather than being derived from
// LC_ALL or LANG: those variables describe what the user declared, not what the
// terminal emulator can draw, and a wrong guess would silently mangle the
// accented database names the substitution exists to protect.
func prepareConsole(out *os.File) (restore func(), asciiOnly bool, err error) {
	return func() {}, false, nil
}

// EnableEscapes is the same statement for a caller that wants escape sequences
// on one stream and nothing else — the command line's progress gauge, which
// rewrites a line on stderr and has no use for raw mode, a code page or a
// frame buffer. Outside Windows a terminal already interprets them, so this
// answers yes without touching anything.
func EnableEscapes(f *os.File) (restore func(), ok bool) {
	return func() {}, true
}
