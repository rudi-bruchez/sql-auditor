# The wizard as the way in — specification

Status: draft, not implemented. Written 12 September 2026.

## The report

An operator copies the release archive to a Windows Server they have just been
given access to, extracts it, and double-clicks `sql-auditor.exe`. A console
window appears and closes again before anything can be read. Nothing asked for
a server name.

The tool they were handed has a full-screen wizard whose first screen edits a
server address and takes a password. It never opens.

## Why it never opens

`cmd/sql-auditor/main.go` decides between three modes, and on a double-click it
decides correctly: no arguments, both ends of the console are a terminal, so
`mode` returns `ModeTUI`. The wizard is then reached through this, at
`main.go:611`:

```go
o, code, err := buildOptionsWithDebug("collect", nil, os.Getenv, os.Stdin, dbg)
if err != nil {
    fmt.Fprintln(os.Stderr, err)
    return code
}
```

`buildOptions` resolves flags, `.env` and the environment through
`collect.Resolve`, which ends with two refusals (`collect/config.go:374`):

```go
if cfg.Server == "" {
    return nil, fmt.Errorf("SQL_SERVER is not set: put it in .env or pass --server")
}
if cfg.User != "" && cfg.Password == "" && !cfg.Integrated {
    return nil, fmt.Errorf("SQL_USER is set but SQL_PASSWORD is empty")
}
```

On a machine with no `.env` the first one fires. The message goes to stderr,
`run` returns 2, the process exits, and Windows closes the console window it
opened for it. The operator sees a flash.

So the wizard's own comment — "lets the operator correct the server and supply a
password on screen 1" — is true of every case except the one it was written
for. A missing server is treated as a configuration the operator wrote and must
go away and correct, when it is in fact the question screen 1 exists to ask.

There is a second failure stacked behind the first. Once the refusal is fixed,
any *other* early exit — a `.env` with an unrecognised key, a parse error, the
usage text of `ModeUsage` — still prints to a console that closes on exit. The
diagnosis is written to a window nobody can read.

## What this specification changes

Four changes, in `collect`, in `tui`, and in `main`. No new module dependency,
no HTTP server, no new command.

### 1. A missing server reaches screen 1 instead of ending the process

`Resolve` stops refusing. Its two final checks move, unchanged in wording, into
a method on the value they test:

```go
// CheckConnectable reports the configuration errors that must be corrected
// before a connection can be attempted. Resolve does not apply it: the wizard
// opens on exactly the configuration that fails it — no .env, no server — and
// screen 1 is where those two are supplied.
func (c *Config) CheckConnectable() error
```

`Resolve` keeps every other refusal it has, including `checkKeys` and the
`--env`-was-typed rule: a `.env` holding a misspelled key is a file the
operator wrote wrongly and no screen can repair it, and a named `.env` that is
absent still stops the run. What moves is only the two that screen 1 can
answer.

Callers:

- `optionsFrom` (`main.go:409`), the only production caller of `Resolve`, calls
  `CheckConnectable` immediately after it **for subcommands**, so `collect`,
  `check`, and every scripted invocation refuse exactly as they do today, with
  the same message and the same exit code 2. This is the behaviour under test;
  it must not move.
- The wizard does not call it at resolution time. It calls it when the operator
  presses Enter on screen 1, on the merged configuration `applyState` produces,
  and a failure becomes `State.ConnError` — the one error in this wizard that
  does not end it. The cursor returns to the field the message is about: the
  server field for a missing server, the password field for a login with no
  password.

`optionsFrom` needs to know which of the two it is resolving for. It already
takes the command name as its first argument; the wizard passes `"collect"`
today, which is what makes the two paths indistinguishable. Add an explicit
parameter rather than overloading that string — a boolean whose name says what
it means, or two thin wrappers over a shared body. The implementer chooses;
what the specification requires is that a reader of `optionsFrom` can see which
caller gets the deferred check without tracing a command name.

### 2. Screen 1 edits the login and the authentication mode

Today screen 1 edits two fields, and says so (`tui/state.go:70`):

```go
// The two editable fields of screen 1, in tab order. They are the only two:
// the database, the authentication mode and the encryption settings come from
// the resolved configuration and the wizard does not edit .env.
const (
	fieldServer = iota
	fieldPassword
	fieldCount
)
```

It becomes four, in this tab order:

| Field | Kind | Edits |
| --- | --- | --- |
| `fieldServer` | text | `State.Server` |
| `fieldAuth` | toggle | `State.Integrated` |
| `fieldUser` | text | `State.User` |
| `fieldPassword` | text, masked | `State.Password` |

The order puts the authentication mode above the two credentials it governs,
because it decides whether they are editable at all.

`User` and `Integrated` leave the read-only block of `State` and join `Server`
and `Password` as edited values. `Catalog`, `Encrypt` and `TrustCert` stay
read-only and stay where they are: none of them stops a first connection, and
each would need its own control.

Behaviour of the toggle:

- Space and Enter both flip it when the cursor is on it. Enter is the submit key
  everywhere else on this screen, and the exception is deliberate: a toggle the
  operator has to know to press Space on is a toggle that looks broken. Enter
  on any other field still submits.
- When `Integrated` is on, `fieldUser` and `fieldPassword` are skipped by Tab
  and drawn dimmed. A Windows integrated connection takes neither, and a field
  that accepts text the connection will discard is a field that lies.
- When it is off and `User` is empty, the screen is submittable — and
  `CheckConnectable` will not refuse it, because an empty `User` with no
  password is the configuration that connects as the current Windows account
  through the driver's own default. That is today's behaviour and it is not
  changed here. What the toggle names is the explicit
  `SQL_INTEGRATED_SECURITY=true`.

`applyState` (`tui/run.go:580`) carries the two new values into the copied
`Config`, beside the server:

```go
cfg.Server = s.Server
cfg.User = s.User
cfg.Integrated = s.Integrated
if s.Password != "" {
    cfg.Password = s.Password
}
```

The password keeps its asymmetry, and the comment explaining it stays: an empty
field means "use what `.env` resolved", not "connect with no password", because
the field starts empty even when `.env` holds one. `User` has no such rule —
it starts populated from the resolved configuration, so an operator who clears
it means it.

`authLine` (`tui/render.go:202`) already renders the mode as a sentence and
needs only to become editable-aware, the way the server line is.

### 3. Saving the connection to `.env`

Screen 1 gains a checkbox, below the fields and above the submit line:

```
    [ ] save the connection to .env
```

Toggled with Space when the cursor reaches it; it is the fifth stop in the tab
order. On a successful connection — not on submit, and not on a refusal — the
file is written.

What is written, and nothing else:

| Key | From | Written when |
| --- | --- | --- |
| `SQL_SERVER` | `State.Server` | always |
| `SQL_USER` | `State.User` | when not integrated and non-empty |
| `SQL_INTEGRATED_SECURITY` | `State.Integrated` | when true |

`SQL_PASSWORD` is never written. `State.Password` keeps the invariant its own
comment states — held in memory, shown as stars, never on disk — and the next
run asks for it again. This is the deliberate difference from sqltop, whose
connect page offers to save the password behind an explicit warning: sqltop is
opened on a workstation by its owner, and this binary is copied onto a client's
server and often left there.

The authentication mode is saved even though it is neither a server nor a
login, because saving a login while dropping the mode that decides whether the
login is used would restore a configuration that does not work.

The writer is new, and lives beside `ParseDotEnv` in `collect/config.go`:

```go
// UpdateDotEnv sets each key in the file at path, preserving every line it
// does not set: comments, blank lines, ordering, and keys it was not given.
// A key already present is rewritten in place; a key absent is appended. The
// file is created at 0600 if it does not exist.
func UpdateDotEnv(path string, set map[string]string) error
```

Requirements on it:

- **In place.** An operator's `.env` carries comments they wrote and keys this
  screen does not edit — `OUTPUT_DIR`, the Query Store window, the encryption
  settings. A writer that regenerates the file from the template destroys them.
  Rewriting a key must preserve the rest of the line's identity: an `export `
  prefix if `ParseDotEnv` accepts one, and the quoting style already in use for
  that key.
- **0600 on creation**, for the reason `WriteEnvTemplate` states at
  `collect/config.go:36`: the file this creates is the one meant to hold
  `SQL_PASSWORD` later, and it is easier to widen a file than to notice it was
  world-readable for a fortnight. It does **not** chmod a file that already
  exists — that file's mode is the operator's, and `WriteEnvTemplate`'s own
  `--force` chmod exists because it truncates, which this does not.
- **Atomic against itself.** Write to a temporary file in the same directory
  and rename over the target. A `.env` truncated halfway by a crash is a
  configuration the operator has to reconstruct, and the run that would have
  told them what was in it has just ended.
- **A refusal is not fatal.** A read-only directory, a `.env` owned by another
  account: the connection succeeded and the collection is about to run. The
  failure is reported on screen — `State` needs a field for it, beside
  `ConnError` but not it — and the wizard proceeds. The operator can write the
  file by hand; they cannot get their connection back.
- The path is the one that was resolved, `--env`'s value or `.env`, and not a
  hardcoded name. A wizard reached with `--env prod.env` is reached with
  arguments, so today this is always `.env`; the path is threaded anyway,
  because the day `--env` becomes compatible with the wizard, a checkbox that
  silently wrote the wrong file would be the bug.

When the file does not exist, it is created with the keys above and one comment
line naming `sql-auditor env init` as the way to get the annotated template.
The template is not written in full: it is 60-odd lines of guidance the operator
did not ask for, and a file this screen created is one they will open expecting
to find what they typed.

### 4. A console opened by Explorer does not close on exit

`tui/screen/terminal_windows.go` already reaches the console API through
`syscall.NewLazyDLL`, and states why it does not import `golang.org/x/sys`:
that would make it a direct dependency and break the tool's claim to one module
beyond the SQL driver. The new predicate joins it there, six lines and one more
`NewProc`:

```go
// LaunchedFromExplorer reports whether this process is alone in its console,
// which is what a double-click from Explorer produces and what no invocation
// from a shell ever does: a shell is itself attached to the console, so the
// list holds at least two.
func LaunchedFromExplorer() bool
```

It calls `GetConsoleProcessList` with a buffer of two and reports whether the
count is exactly 1. A count of 0 is the API's error return and reports false: an
unreadable console is not a reason to hold a window open. `terminal_other.go`
gets a stub returning false — on a Unix desktop the question does not arise,
because nothing there launches a console binary by double-click and closes the
terminal under it.

The hold goes in `main()`, around `run()`:

```go
func main() {
	code := run()
	if screen.LaunchedFromExplorer() {
		holdConsole(os.Stdout, os.Stdin)
	}
	os.Exit(code)
}
```

In `main` and not in a branch, because the branches are exactly what is wrong
today: the configuration refusal, `ModeUsage`, and the wizard's own error paths
each need it, and a fix applied to the one in the report leaves the others
closing on an unread message.

`holdConsole` prints a blank line and `press Enter to close this window`, then
reads until a newline or EOF. It ignores read errors — the window is already
open and the process is already finished; there is nothing left to fail.

It runs on every exit and not only on failure. A successful wizard run ends
with the final screen, which the operator wants to read: it names the archive
path they are about to send.

## What the operator sees afterwards

A double-click on a server with no `.env`:

```
    Server     [SQL01_______________________]
    Auth       ( ) Windows integrated  (o) SQL login
    Login      [AUDIT_RO____________________]
    Password   [*********___________________]
    Database   the login's default
    Encrypted  no

    [ ] save the connection to .env

    Read from .env and the environment.
```

Enter attempts the connection. A refusal reprints the server's own message and
returns the cursor to the field it concerns. A success writes `.env` if the box
is checked, and moves to the permission preflight, unchanged.

A double-click on a server whose `.env` has a misspelled key:

```
unrecognised setting(s): SQL_SERVERS

press Enter to close this window
```

## What this does not do

**No web interface.** sqltop answers the same question with a connect page on
loopback, and it is the right answer there: sqltop's product *is* a live
interface, so the HTTP server, the per-run token and the socket handoff between
the connect phase and the monitor all pay for themselves several times over.
Its `internal/web` is about 9,800 lines with its tests. sql-auditor collects and
exits. Buying a second presentation layer to replace a screen this repository
already has — and then maintaining both — is the trade the report does not
justify. If a future requirement is a form on a machine with no console at all,
that is a different specification, and it starts by asking whether the wizard
should be retired rather than duplicated.

**No Authenticode signature.** Distinct problem, distinct answer, and it is not
this one: signing does not remove the SmartScreen prompt for a new publisher,
it only names them. `README.md` documents `tar -xf` for unpacking, which
removes the prompt's actual cause on a fresh server.

**No editing of the database, the encryption settings or the certificate
trust.** They stay read-only on screen 1, and `.env` or a flag remains the way
to set them. Each would need its own control and none of them blocks a first
connection.

**No password in `.env`, and no prompt offering to put one there.**

## Testing

Every one of the four changes has a check that fails if it regresses.

1. **`Resolve` and `CheckConnectable`.** A table over the two refusals: each
   must still be produced, by `CheckConnectable`, with today's wording; and
   `Resolve` must now return a `*Config` for a configuration with no server at
   all. The four existing test files that call `Resolve` say what the current
   contract is — read them before moving anything, and the ones asserting a
   refusal from `Resolve` move to the new method rather than being deleted.
2. **Screen 1.** A table over the tab order with `Integrated` on and off, which
   is what proves the two credential fields are skipped rather than merely
   dimmed; a case for the toggle on Space and on Enter; and a case that Enter
   on a text field still submits. `tui/state.go` is a pure function of `State`
   and `tui/state_test.go` already tests it that way.
3. **`UpdateDotEnv`.** A round trip over a fixture `.env` carrying comments, a
   blank line, a quoted value, an `export ` prefix if the parser accepts one,
   and two keys this screen never sets: after setting `SQL_SERVER` over an
   existing value and appending `SQL_INTEGRATED_SECURITY`, every other line must
   be byte-identical and `ParseDotEnv` must read the result back to the expected
   map. Plus a case for the file that does not exist, and one for a write that
   fails, which must return an error and not a panic.
4. **The console hold.** `LaunchedFromExplorer` cannot be tested — no test here
   can allocate a console, which is the reason `isTTY` is already injected into
   `mode`. So `holdConsole` takes its reader and writer as parameters and is
   tested on a `strings.Reader`, and the predicate stays a one-line call with
   nothing in it to get wrong.

`go vet ./...` and `go test ./... -count=1` pass, as the release workflow
requires before it will publish.

## Open questions

**Does the checkbox belong on screen 1 or the final screen?** It is on screen 1
here, because that is where the values are typed and where an operator is
thinking about the connection. The argument for the final screen is that it is
reached only after the connection is known to work, so the offer would never be
made for a connection that failed. Screen 1 wins on the grounds that an
operator who has finished a collection has stopped thinking about
configuration; the write still happens only on success.

**Should a saved `.env` cause the next double-click to skip screen 1?** No, and
this is not an open question so much as a temptation to record: the screen shows
what `.env` resolved to, which is exactly the confirmation an operator wants
before pointing the tool at a production instance. `ServerFrom` exists in
`Config` because that precedence has already misled the author of this program
once (`collect/config.go:61`). Screen 1 stays.
