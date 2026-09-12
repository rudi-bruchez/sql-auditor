# The wizard as the way in — specification

Status: draft, not implemented. Written 12 September 2026, revised the same day
after a panel of external reviews. Every line reference below was re-checked
against the tree at that revision; the first draft's were off by one.

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
`main.go:610`:

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

This was confirmed end to end by a reviewer who launched the binary the way a
double-click launches it — alone in a new console, no arguments, no `.env`,
stderr redirected so the outcome survived the window:

```
debug   +0.5ms  stdin tty=true, stdout tty=true, SQL_AUDITOR_NO_TUI="", args=0 → wizard
debug   +1.1ms  .env absent; the environment alone then
debug   +1.1ms  configuration refused: SQL_SERVER is not set: put it in .env or pass --server
```

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

`Resolve` stops refusing. Its two final checks move into a method on the value
they test:

```go
// ErrNoServer and ErrNoPassword are sentinels so a caller can route on which
// refusal it got without matching the message text. The wizard needs exactly
// that: one puts the cursor in the server field, the other in the password
// field.
var (
    ErrNoServer   = errors.New("SQL_SERVER is not set: put it in .env or pass --server")
    ErrNoPassword = errors.New("SQL_USER is set but SQL_PASSWORD is empty")
)

// CheckConnectable reports the configuration errors that must be corrected
// before a connection can be attempted. Resolve does not apply it: the wizard
// opens on exactly the configuration that fails it — no .env, no server — and
// screen 1 is where those two are supplied.
func (c *Config) CheckConnectable() error
```

The two messages keep their present wording, because scripted callers see them
today and a message is an interface. Sentinels rather than strings: the wizard
must decide which field to put the cursor in, and a screen that string-matched
a message owned by `collect` would break the next time that message improved.

`Resolve` keeps every other refusal it has, including `checkKeys` and the
malformed-value errors it returns through `firstErr`. The `--env`-was-typed
rule is not in `Resolve` at all — it is in `optionsFrom`, and it stays there.
What moves is only the two refusals that screen 1 can answer.

**The seam.** The wizard does not call `optionsFrom` directly; it calls
`buildOptionsWithDebug`, which calls `optionsFrom`. Threading a flag through
both would touch every one of the two dozen test call sites. Instead, two named
wrappers over the shared body:

```go
// buildOptionsForCommand resolves and refuses, as every subcommand requires.
func buildOptionsForCommand(cmd string, args []string, env func(string) string, stdin io.Reader, dbg *debugLog) (collect.Options, int, error)

// buildOptionsForWizard resolves without applying CheckConnectable, because
// screen 1 is where a missing server and a missing password are supplied.
func buildOptionsForWizard(env func(string) string, stdin io.Reader, dbg *debugLog) (collect.Options, int, error)
```

Two functions whose names say which behaviour they carry, and no boolean to
trace. `optionsFrom` (`main.go:329`, with the `Resolve` call at `main.go:409`)
gains the check under the first wrapper only.

Subcommand behaviour does not move: `collect` and `check` refuse with the same
message and exit code 2. Nothing else reaches `Resolve` — `env init`,
`queries export`, `nothingToDo`, `--password-file` and `--password-stdin` were
each checked and do not. `readPassword`'s errors still precede the
connectability refusal, and `firstErr` still wins over it, because `Resolve`
returns it first.

**Where the wizard applies it.** Not in `keyConnection`, which is a pure
function of `State` and has no `Options` to check. In `runner.connect`, which
already calls `applyState` and already owns `connectFailedEvent`: it calls
`CheckConnectable` on the merged configuration before opening a connection, and
a refusal becomes a `connectFailedEvent` like any other. The existing
`connectFailed` always returns the cursor to `fieldServer`
(`tui/state.go:316`); it gains a case for `ErrNoPassword`, and
`TestARefusedConnectionStaysOnTheConnectionScreen` is amended rather than
merely joined by a new test.

### 2. Screen 1 edits the login

Today screen 1 edits two fields, and says so (`tui/state.go:71`):

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

It becomes four stops, in this tab order:

| Stop | Kind | Edits |
| --- | --- | --- |
| `fieldServer` | text | `State.Server` |
| `fieldUser` | text | `State.User` |
| `fieldPassword` | text, masked | `State.Password` |
| `fieldSaveEnv` | checkbox | `State.SaveEnv` (change 3) |

`fieldCount` becomes 4 and every stop is reachable, so `keyConnection`'s
`s.Field = (s.Field + 1) % fieldCount` keeps working unchanged. `editField`
(`tui/state.go:300`) is a two-way branch today — password, else server — and
must gain a `fieldUser` case and, for the two non-text stops, do nothing rather
than fall through to the server. A rune typed on the checkbox that edited the
server address would be the sort of defect nobody reproduces on purpose.

**There is no authentication toggle.** An earlier draft had one. It is not
needed: `connURL` attaches credentials only when a login is set and integrated
security is off (`collect/runner.go:276`),

```go
if cfg.User != "" && !cfg.Integrated {
    u.User = url.UserPassword(cfg.User, cfg.Password)
}
```

and the pinned driver closes the argument from the other end: in
`integratedauth/winsspi/winsspi.go`, `getAuth` returns the current-account SSPI
authenticator exactly when `config.User == ""`. So an empty login *already is*
a Windows integrated connection, in the driver and not merely by convention.
(`collect/tls.go:56` tests the same predicate, but it composes a warning about
certificate trust and is not the connection path. An earlier draft cited it;
it proves nothing about authentication.) A toggle would
have bought a redundant control and cost the dimming of skipped fields, a
non-dense tab order, an Enter that means something different on one stop, and a
`.env` key a save could set and no save could clear. `Integrated` therefore
stays read-only, alongside `Catalog`, `Encrypt` and `TrustCert`: none of them
blocks a first connection, and `SQL_INTEGRATED_SECURITY` remains a `.env` key
for the operator who wants it explicitly.

Nothing is dimmed and no stop is skipped. `tui/render.go` emits no escape
sequence of its own and has no colour anywhere in it (`render.go:53`,
`render.go:90`), which is what makes its output safe to paste into an email and
`NO_COLOR` trivially honoured. A dimmed field would have meant either breaking
that invariant or inventing a textual convention for it; with a dense tab order
neither is needed.

**`applyState` and the inherited password.** This is the part the panel caught,
and it is the reason the login was not simply made editable. `applyState`
(`tui/run.go:580`) keeps the resolved `.env` password when `State.Password` is
empty, because the field starts empty even when `.env` holds one. With a
read-only login that rule was safe: the inherited password always belonged to
the displayed login. An editable login breaks it. A reviewer ran the first
draft's fragment verbatim:

```
[claim4] connection attempted with user="auditor2" password="OldSecret" -> CheckConnectable: <nil>
[claim4] cleared login: user="" password="OldSecret" -> CheckConnectable: <nil>
```

A login the operator typed, authenticated with a password they never typed,
against a production instance, and nothing refuses it. So:

```go
cfg.Server = s.Server
cfg.User = s.User
switch {
case s.Password != "":
    cfg.Password = s.Password
case s.User != o.Config.User:
    // The login on screen is not the one .env resolved, so .env's password
    // does not belong to it. Dropping it makes CheckConnectable refuse with
    // the cursor in the password field, which is the question to ask; carrying
    // it over would send a stranger's password to the server instead.
    cfg.Password = ""
}
```

The cleared-login case falls out of the same rule: `State.User` differs from
the resolved one, the inherited password is dropped, and an empty login with no
password is an integrated connection, which is what an operator who cleared the
field meant.

`authLine` (`tui/render.go:202`) already prints the mode and the login on one
line. The login moves to its own editable row and `authLine` keeps only the
mode.

### 3. Saving the connection to `.env`

Screen 1 gains a checkbox, below the fields and above the submit line:

```
    [ ] save the connection to .env
```

Toggled with Space; it is the fourth tab stop. On a successful connection — not
on submit, and not on a refusal — the file is written.

What is written, and nothing else:

| Key | From | Written as |
| --- | --- | --- |
| `SQL_SERVER` | `State.Server` | the typed value |
| `SQL_USER` | `State.User` | the typed value, or an empty value when cleared |

`SQL_PASSWORD` is never written. `State.Password` keeps the invariant its own
comment states — held in memory, shown as stars, never on disk — and the next
run asks for it again. This is the deliberate difference from sqltop, whose
connect page offers to save the password behind an explicit warning: sqltop is
opened on a workstation by its owner, and this binary is copied onto a client's
server and often left there.

`SQL_USER` is written **even when empty**, as `SQL_USER=`, and this matters.
An operator who saved a login and then cleared it must not be left with a stale
one: `SQL_USER` set with no password makes the next `sql-auditor collect` exit
2 with "SQL_USER is set but SQL_PASSWORD is empty", which is a tool that
refuses to start because of a screen the operator used correctly. An empty
value resolves to an empty login, which is the integrated connection they
chose.

The writer is new, and lives beside `ParseDotEnv` in `collect/config.go`:

```go
// UpdateDotEnv sets each key in the file at path, preserving every line it
// does not set: comments, blank lines, ordering, and keys it was not given.
// Every occurrence of a key it sets is rewritten; a key absent is appended.
// The file is created if it does not exist.
func UpdateDotEnv(path string, set map[string]string) error
```

Requirements on it:

- **In place, and every occurrence.** `ParseDotEnv` is last-wins
  (`collect/config.go:166`). A writer that rewrote only the first occurrence
  would leave a later duplicate governing: the save reports success and the
  next run resolves the old server. An operator who appended a correction to
  the bottom of their `.env` without deleting the old line has exactly that
  file. Rewrite them all, and put a duplicate key in the test fixture.
- **Preserve the rest of the line.** An `export ` prefix, which `ParseDotEnv`
  accepts, and the quoting style already in use for that key. The parser has no
  escape support — a `"` closes the value regardless of what precedes it — so a
  value that cannot survive the existing style is written unquoted. The three
  values this writer emits never need quoting; the rule exists so that the
  implementer does not invent one under pressure.
- **The existing file is truncated and rewritten, not replaced.** The first
  draft asked for a temporary file renamed over the target *and* for an
  existing file's permissions to be left alone. Two reviewers showed
  independently that those are contradictory: `os.Rename` replaces the file
  object, so an explicit ACL is discarded in favour of directory-inherited
  ones. Measured on a `.env` whose ACL had been narrowed to its owner:

  ```
  before: .env  DALEK\rudi:(R,W)
  after temp+rename: DALEK\CodexSandboxUsers:(I)(M), BUILTIN\Administrateurs:(I)(F), …
  ```

  The explicit entry is gone and the file is group-readable — on the one file
  meant to hold a password. So an existing file is opened `O_WRONLY|O_TRUNC`
  and rewritten through the same file object, which keeps its security
  descriptor and its POSIX mode. That is not atomic, and the window is
  deliberately accepted: the content is a few hundred bytes written in one
  call, and losing the operator's ACL is the worse outcome of the two. A file
  that does not exist is created at 0600.
- **0600 means nothing on Windows.** Go maps the perm argument to the
  read-only attribute and NTFS ACLs govern actual access — `main.go:295`
  already says as much about this platform. The mode is kept for POSIX and for
  consistency with `WriteEnvTemplate`; it is not a protection on the platform
  where this binary spends most of its life, and the specification does not
  claim otherwise.
- **A refusal is not fatal.** A read-only directory, a `.env` open in another
  process (verified: the write then fails with "Access is denied"): the
  connection succeeded and the collection is about to run. The failure is
  reported on screen — `State` needs a field for it, beside `ConnError` and not
  it — and the wizard proceeds. The operator can write the file by hand; they
  cannot get their connection back.
- **The path must be threaded, because none exists.** Neither
  `collect.Options` nor `collect.Config` carries the `.env` path today, and
  `tui.Run` takes nothing else. `optionsFrom` knows it — it is `c.envFile` —
  so `collect.Options` gains a field for it and `initialState` carries it onto
  `State`. Not a hardcoded `".env"`: the wizard is unreachable with arguments
  today, so the resolved path is always `.env`, and the day that changes a
  checkbox that silently wrote the wrong file would be the bug.

When the file does not exist it is created with the keys above and one comment
line naming `sql-auditor env init` as the way to get the annotated template.
The template is not written in full: it is 60-odd lines of guidance the operator
did not ask for, and a file this screen created is one they will open expecting
to find what they typed.

All three keys involved are already in `knownKeys` (`collect/config.go:112`),
and the comment line is skipped by the parser, so a file this writer produces
survives the next run's `checkKeys`. That was verified, not assumed.

### 4. A console opened by a double-click does not close on an unread message

This change is much narrower than the first draft's, which three reviewers
between them showed would have made the reported scenario worse rather than
better. What went wrong is recorded below the requirement, because the reasoning
that produced it was plausible and will recur.

**The requirement.** When an invocation that was *going to* print a diagnosis
and exit — a configuration refusal, `ModeUsage`, any early error before the
wizard takes the screen — is running alone in its console, hold the window open
until a key is pressed.

Three conditions, all of them necessary:

- **No argument was given.** A run with arguments is a scripted run, whatever
  its console looks like, and it must never wait for a keystroke.
- **The process is alone in its console.** `GetConsoleProcessList` returns 1.
- **The wizard was never entered.** The hold happens on the paths that return
  before `tui.Run`, never after it.

So the hold is not in `main` around `run()`, as the first draft had it. It sits
on the two early-exit paths inside `run`'s `switch m` — `ModeUsage`, and
`ModeTUI`'s configuration refusal — after the message is printed and before the
return.

`tui/screen/terminal_windows.go` already reaches the console API through
`syscall.NewLazyDLL`, and states why it does not import `golang.org/x/sys`:
that would make it a direct dependency and break the tool's claim to one module
beyond the SQL driver. The new predicate joins it there, six lines and one more
`NewProc`:

```go
// AloneInConsole reports whether this process is the only one attached to its
// console. A count of 0 is the API's error return — a process with no console
// at all — and reports false: an unreadable console is not a reason to wait
// for a keystroke.
func AloneInConsole() bool
```

It is named for what it measures, not for what it is used to infer. The first
draft called it `LaunchedFromExplorer`, which is false in both directions, and
naming it that way is what let the false inference through review.
`terminal_other.go` gets a stub returning false.

`holdConsole(w io.Writer, r io.Reader)` prints a blank line and `press Enter to
close this window`, then reads until a newline or EOF. It ignores read errors.
Its reader and writer are parameters so it can be tested.

**What the first draft got wrong, and why.**

*It would have hung every scheduled collection.* Measured by two reviewers
independently: anything that gets its own console gives a count of 1 —
`Start-Process`, `cmd /c start /wait`, a scheduled task set to run only when
the user is logged on. With the hold in `main` around every exit, a nightly
`sql-auditor collect` under such a task would finish its work and then block
forever on a console nobody is watching:

```
Start-Process .\hold.exe -Wait
start-process-new-console: explorer=true start
start-process-new-console: holdConsole STILL BLOCKED after 4s   # would be forever
```

The "no argument was given" condition is what removes this, and it is the
condition the first draft lacked because it reasoned about consoles instead of
about invocations.

*It could not have been released after a wizard run.* `tui/run.go`'s `readKeys`
goroutine is left parked inside `Read(os.Stdin)` when `tui.Run` returns, and
`tui/run.go:190` says so explicitly: "That is deliberate rather than
overlooked. `tui.Run` returns to main, which exits within microseconds, so the
goroutine outlives the wizard by nothing." A second reader of the same handle
in `main` competes with a read that is already blocked, and loses. A reviewer
reproduced it with the draft's own `holdConsole`: both keystrokes went to the
parked goroutine and the hold never returned. The "wizard was never entered"
condition removes this, and it is the reason the hold is not in `main`.

*Its stated benefit on success did not exist.* The draft held on every exit so
that "a successful wizard run ends with the final screen, which the operator
wants to read: it names the archive path". Both false. `Terminal.Close` clears
the screen on the way out (`tui/screen/terminal.go:107`, and `tui/run.go:100`
repeats it), and under the wizard `OwnsScreen` suppresses the archive path
`collect.Run` would otherwise print (`tui/run.go:537`). The held window would
have shown the run-log path and nothing else. See the open question below,
because the operator not being shown the archive path is a real defect — it is
simply not this one.

**A case this does not cover, stated rather than hidden.** A launcher of the
form `cmd /c sql-auditor.exe` — a shortcut, a `.bat`, any wrapper — puts two
processes in the console, so the predicate is false, no hold happens, and the
window still closes on the message. That is a real gap. It is left open because
every way to close it that was considered widens the predicate until it catches
the scheduled runs this change exists to protect, and a `.bat` author can add
`pause`. The operator in the report double-clicks the `.exe`, which is covered.

## What the operator sees afterwards

A double-click on a server with no `.env`:

```
    Server     [SQL01_______________________]
    Login      [AUDIT_RO____________________]
    Password   [*********___________________]
    Database   master
    Auth       SQL login
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
Its `internal/web` is several thousand lines with its tests. sql-auditor
collects and exits. Buying a second presentation layer to replace a screen this
repository already has — and then maintaining both — is the trade the report
does not justify. If a future requirement is a form on a machine with no
console at all, that is a different specification, and it starts by asking
whether the wizard should be retired rather than duplicated.

**No Authenticode signature.** Distinct problem, distinct answer, and it is not
this one: signing does not remove the SmartScreen prompt for a new publisher,
it only names them. `README.md` documents `tar -xf` for unpacking, which
removes the prompt's actual cause on a fresh server.

**No editing of the database, the authentication mode, the encryption settings
or the certificate trust.** They stay read-only on screen 1, and `.env` or a
flag remains the way to set them. None of them blocks a first connection.

**No password in `.env`, and no prompt offering to put one there.**

**No hold for a console shared with a wrapper process,** as stated in change 4.

## Testing

Every one of the four changes has a check that fails if it regresses.

1. **`Resolve` and `CheckConnectable`.** A table over the two refusals: each
   must still be produced, by `CheckConnectable`, matching its sentinel; and
   `Resolve` must now return a `*Config` for a configuration with no server at
   all. Note that only the server refusal has a test today
   (`TestResolveRequiresServer`, `collect/config_test.go:87`) — the
   `SQL_USER`-without-password refusal has none, so this change adds the test
   that was missing rather than moving one that existed. And
   `TestEmbeddedEnvTemplateIsAcceptedByTheResolver` (`queries_test.go:184`)
   must additionally assert `CheckConnectable` on the resolved template: as
   written it would stay green for a template that had lost `SQL_SERVER`
   entirely, which is the one thing it exists to catch.
2. **Screen 1.** A table over the four-stop tab order; a case that a rune typed
   on `fieldUser` edits the login and a rune typed on `fieldSaveEnv` edits
   nothing; and the inherited-password table — resolved `.env` login with a
   password, then (a) password typed, (b) login edited and password left empty,
   (c) login cleared — asserting what `applyState` produces and what
   `CheckConnectable` then says about it. Case (b) is the defect the panel
   found and the test that must fail before the fix.
3. **`UpdateDotEnv`.** A round trip over a fixture `.env` carrying comments, a
   blank line, a quoted value, an `export ` prefix, **a duplicate
   `SQL_SERVER`**, and two keys this screen never sets: after setting
   `SQL_SERVER` and `SQL_USER`, every other line must be byte-identical, both
   occurrences of the duplicate must be rewritten, and `ParseDotEnv` must read
   the result back to the expected map. Plus: a file that does not exist; an
   empty `SQL_USER` written and read back as an empty login; and a write that
   fails, which must return an error the wizard reports rather than panic.
   On POSIX, a case asserting that an existing file's mode survives the write.
4. **The console hold.** `AloneInConsole` cannot be tested — no test here can
   allocate a console, which is the reason `isTTY` is already injected into
   `mode`. So the decision is a pure function of (argument count, alone,
   entered-wizard) and is tabled, and `holdConsole` is tested on a
   `strings.Reader`. The predicate stays a one-line call with nothing in it to
   get wrong.

`go vet ./...` and `go test ./... -count=1` pass, as the release workflow
requires before it will publish.

## Open questions

**The archive path is not shown after a wizard run, and that is a separate
defect.** Found by the panel while refuting change 4's original justification:
`Terminal.Close` erases the final frame and `OwnsScreen` suppresses the line
`collect.Run` would have printed, so an operator who completes a collection
through the wizard is never shown the path of the archive they are meant to
send. `flushOnExit` already writes the run-log path to stderr after the
terminal is restored, which is where the archive path belongs too. It is one
line and it is not in this specification, because it is not about getting in.
It should be its own change, and it should not wait long.

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
