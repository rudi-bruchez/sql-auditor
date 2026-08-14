package screen

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// The console API is reached through syscall.NewLazyDLL rather than through
// golang.org/x/sys/windows on purpose. Importing that package would make x/sys
// a *direct* dependency — `go mod tidy` would drop its `// indirect` marker —
// and this tool's public argument is that it pulls in one module beyond the SQL
// driver, not two. Twenty lines of kernel32 keep that promise, and the four
// calls below are the entire surface used.
var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
)

const (
	enableVirtualTerminal = uint32(0x0004) // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	codePageUTF8          = uint32(65001)
)

// EnableEscapes turns on virtual terminal processing for one stream and
// returns the undo, for a caller that wants escape sequences and nothing else:
// the command line's progress gauge rewrites a line on stderr and has no use
// for raw mode, a code page or a frame buffer.
//
// It exists because the flag is per handle and OFF by default on conhost. The
// wizard sets it on stdout; a gauge writing "\r\x1b[K" to stderr without asking
// prints those bytes literally — "←[K[ 12/223] …" once a second on a Windows
// Server console reached over RDP, which is this tool's ordinary setting.
//
// ok=false is an answer, not a failure: the caller degrades to plain lines
// rather than painting a screen the console cannot draw.
func EnableEscapes(f *os.File) (restore func(), ok bool) {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return func() {}, false
	}
	if mode&enableVirtualTerminal != 0 {
		return func() {}, true
	}
	if r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminal)); r == 0 {
		return func() {}, false
	}
	// Restore the mode wholesale rather than clearing our bit: another
	// component may have set the same flag for its own reasons, and the
	// contract here is "the console as we found it".
	return func() { _, _, _ = procSetConsoleMode.Call(uintptr(h), uintptr(mode)) }, true
}

// prepareConsole makes the output handle understand ANSI and speak UTF-8, and
// returns the undo. It is what the wizard needs and EnableEscapes is not: a
// full-screen renderer also depends on the code page, and it cannot degrade.
// The two failures here are not equivalent, which is why only one is an error:
//
//   - No virtual terminal processing means no escape sequences, so no cursor
//     addressing and no clear — there is no scrolling fallback renderer, and
//     building one would be a second engine with its own golden strings for a
//     conhost that has not shipped since Windows 10 1511. Open fails and the
//     operator is sent to `sql-auditor collect`.
//   - No UTF-8 code page only means the box drawing of names is wrong. A French
//     client with a COMPTABILITÉ database would read mojibake, which is bad but
//     recoverable: the caller degrades to the strict ASCII substitutions and
//     carries on.
func prepareConsole(out *os.File) (restore func(), asciiOnly bool, err error) {
	h := syscall.Handle(out.Fd())

	var mode uint32
	r, _, callErr := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return func() {}, false, fmt.Errorf("this terminal has no console mode, so ANSI cannot be enabled: %w", callErr)
	}
	if mode&enableVirtualTerminal == 0 {
		if r, _, callErr := procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminal)); r == 0 {
			return func() {}, false, fmt.Errorf("this console cannot enable ANSI escape sequences: %w", callErr)
		}
	}
	priorMode := mode

	// GetConsoleOutputCP has no failure return worth branching on: it answers 0
	// only when there is no console, and we have already established there is
	// one. A zero would make the restore a no-op, which is the right outcome.
	priorCP, _, _ := procGetConsoleOutputCP.Call()
	if r, _, _ := procSetConsoleOutputCP.Call(uintptr(codePageUTF8)); r == 0 {
		asciiOnly = true
	}

	return func() {
		if priorCP != 0 {
			_, _, _ = procSetConsoleOutputCP.Call(priorCP)
		}
		// Restore the mode wholesale rather than clearing our bit: another
		// component may have set the same flag for its own reasons, and the
		// contract here is "the console as we found it".
		_, _, _ = procSetConsoleMode.Call(uintptr(h), uintptr(priorMode))
	}, asciiOnly, nil
}
