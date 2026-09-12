# Code Review: Implementation of `docs/connect-screen-spec.md`

This document contains the review findings and actionable implementation tasks for the `connect-screen-spec` branch. Pass this file to Codex to apply the necessary corrections and complete test coverage.

---

## Executive Summary

The implementation in branch `connect-screen-spec` successfully delivers the four architectural pillars defined in `docs/connect-screen-spec.md`:
1. Decoupled connectability validation (`CheckConnectable`) with sentinels `ErrNoServer` and `ErrNoPassword`.
2. Editable login field on Screen 1 with four tab stops and security guard against password inheritance across login changes.
3. In-place rewriting of `.env` through `UpdateDotEnv` preserving ACLs, inline comments, quotes, and duplicates.
4. Windows console hold (`AloneInConsole` + `holdConsole`) preventing window closure on early diagnostic exits.

However, several defects and missing specification test requirements must be addressed before merging.

---

## Priority 1: Flaky Test & Non-Deterministic Map Iteration in `UpdateDotEnv`

### Location
- `collect/config.go:241-245`
- `collect/config_test.go:164-182`

### Issue
When appending new keys (or when creating a new `.env` file), `UpdateDotEnv` iterates over the `set map[string]string` parameter directly:
```go
for key, value := range set {
    if !found[key] {
        result += key + "=" + value + "\n"
    }
}
```
In Go, map iteration order is intentionally randomized. When `UpdateDotEnv` creates a new file with `SQL_SERVER` and `SQL_USER`, the order of lines is non-deterministic (`SQL_SERVER` first or `SQL_USER` first).

Running:
```bash
go test -count=100 -run TestUpdateDotEnv ./collect
```
consistently fails `TestUpdateDotEnvCreatesOnlyConnectionSettings` when `SQL_USER=` is emitted before `SQL_SERVER=SQL01`.

### Required Fix
Collect and sort the unencountered keys before appending them:
```go
var unwritten []string
for key := range set {
    if !found[key] {
        unwritten = append(unwritten, key)
    }
}
sort.Strings(unwritten)
for _, key := range unwritten {
    result += key + "=" + set[key] + "\n"
}
```
Sort order places `SQL_SERVER` before `SQL_USER`, ensuring deterministic output and passing `go test -count=100`.

---

## Priority 2: `SaveError` Is Never Rendered to the User

### Location
- `tui/render.go:165-167`
- `tui/run.go:230-245`
- `tui/state.go:270-275`

### Issue
`docs/connect-screen-spec.md` (lines 324-329) states:
> *"A refusal is not fatal. A read-only directory, a `.env` open in another process ... the connection succeeded and the collection is about to run. The failure is reported on screen — `State` needs a field for it, beside `ConnError` and not it — and the wizard proceeds."*

In the current implementation:
1. `runner.connect` writes `.env` only after a connection succeeds (`collect.Open` succeeds).
2. Upon success, `connectedEvent` is dispatched, setting `s.SaveError = e.saveErr` and transitioning `s.Step = StepVerifying` (Screen 2).
3. In `tui/render.go`, `s.SaveError` is only rendered inside `renderConnection` (Screen 1).
4. Because the wizard transitions to Screen 2, `renderConnection` is never painted again unless the user manually presses `b`. The operator is never informed that saving `.env` failed.
5. In `tui/state.go:keyConnection`, `KeyEnter` resets `s.ConnError = nil` but fails to reset `s.SaveError = nil`.

### Required Fix
1. **Render `SaveError` on Screen 2:**
   In `tui/render.go:renderVerification` (or inside `localBlock`), check `s.SaveError != nil` and display a prominent warning banner or row (e.g., `row(pad+"Env", "!! save failed", width)` or `Could not save .env: <err>`).
2. **Clear on Retry:**
   In `tui/state.go:keyConnection` under `case screen.KeyEnter:`, add `s.SaveError = nil`.

---

## Priority 3: Missing Specification Tests

The test plan in `docs/connect-screen-spec.md` (lines 498-535) requires specific test cases that are currently missing:

### 3.1. POSIX File Permissions Preservation in `UpdateDotEnv`
- **Location:** `collect/config_test.go`
- **Requirement (Spec § 3, l. 528):**
  > *"On POSIX, a case asserting that an existing file's mode survives the write."*
- **Required Task:** Add a test (guarded by `if runtime.GOOS == "windows" { t.Skip(...) }`) that creates a `.env` file with specific permissions (e.g. `0o640`), executes `UpdateDotEnv`, and asserts with `os.Stat` that `info.Mode().Perm()` remains `0o640`.

### 3.2. Tab Stop 4 (`fieldSaveEnv`) Keystroke Isolation & Toggle
- **Location:** `tui/state_test.go`
- **Requirement (Spec § 2, l. 513-515):**
  > *"a case that a rune typed on `fieldUser` edits the login and a rune typed on `fieldSaveEnv` edits nothing"*
- **Required Task:**
  - Update or complement `TestTypingGoesToTheSelectedField`: navigate with Tab to `fieldSaveEnv`, send typed runes (`'x'`, `'1'`), and assert that neither `Server`, `User`, nor `Password` was altered.
  - Assert that sending `screen.KeySpace` while on `fieldSaveEnv` toggles `State.SaveEnv` between `false` and `true`.

### 3.3. Amend `TestARefusedConnectionStaysOnTheConnectionScreen` for `ErrNoPassword`
- **Location:** `tui/state_test.go:261`
- **Requirement (Spec § 1, l. 137-138):**
  > *"it gains a case for `ErrNoPassword`, and `TestARefusedConnectionStaysOnTheConnectionScreen` is amended rather than merely joined by a new test."*
- **Required Task:** Amend `TestARefusedConnectionStaysOnTheConnectionScreen` to verify both:
  - An arbitrary connection refusal (`errConnRefusedForTest`) lands cursor on `fieldServer`.
  - A refusal wrapping `collect.ErrNoPassword` lands cursor on `fieldPassword`.

### 3.4. Direct Test of `buildOptionsForWizard`
- **Location:** `cmd/sql-auditor/main_test.go`
- **Requirement (Spec § 1, l. 504):**
  > *"Resolve must now return a `*Config` for a configuration with no server at all."*
- **Required Task:** Add a unit test in `cmd/sql-auditor/main_test.go` verifying that `buildOptionsForWizard(noEnv, noStdin, nil)` returns a valid `Options` without error when `SQL_SERVER` is unset, whereas `buildOptions("collect", nil, noEnv, noStdin)` returns `code == 2` and `ErrNoServer`.

---

## Priority 4: Visual Layout Alignment on Screen 1

### Location
- `tui/render.go:148-154`

### Issue
The specification mockup (lines 448-454) groups the three editable fields together before the read-only metadata:
```text
    Server     [SQL01_______________________]
    Login      [AUDIT_RO____________________]
    Password   [*********___________________]
    Database   master
    Auth       SQL login
    Encrypted  no
```
In the current implementation:
```go
fieldPad + fmt.Sprintf("%-11s%s", "Server", editable(s.Server, s.Field == fieldServer)),
fieldPad + fmt.Sprintf("%-11s%s", "Login", editable(s.User, s.Field == fieldUser)),
fieldPad + fmt.Sprintf("%-11s%s", "Database", s.Catalog),
fieldPad + fmt.Sprintf("%-11s%s", "Auth", authLine(s)),
fieldPad + fmt.Sprintf("%-11s%s", "Password", editable(mask(s.Password), s.Field == fieldPassword)),
fieldPad + fmt.Sprintf("%-11s%s", "Encrypted", encryptionLine(s)),
```
`Database` and `Auth` separate `Login` and `Password`. Navigating with Tab from `Login` (stop 1) to `Password` (stop 2) causes the focus indicator to visually jump over two static lines.

### Required Fix
Reorder the rendered rows in `renderConnection` to place `Password` immediately after `Login`, matching the spec mockup and the sequential tab order.

---

## Acceptance Verification Checklist for Codex

Before submitting changes, run and verify:

1. `go test -count=100 -run TestUpdateDotEnv ./collect` (must pass 100/100 times).
2. `go test -count=1 ./...` (must pass clean across all packages).
3. `go vet ./...` (must exit 0).
4. Verify that `git grep -niE "<any client names>"` returns no results (per `CLAUDE.md`).
5. Ensure commit messages are written in English.
