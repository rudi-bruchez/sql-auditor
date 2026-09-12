# Connect screen: response to code review

This note records the corrections made after reviewing
`docs/review-connect-screen.md`. It is intended for a focused follow-up review.

## Corrections applied

### Deterministic `.env` creation

`collect.UpdateDotEnv` now gathers keys absent from the existing file, sorts
them, then appends them. This removes Go map-iteration nondeterminism and makes
a newly-created connection file consistently contain `SQL_SERVER` before
`SQL_USER`.

The existing-file behaviour is unchanged: every occurrence of a requested key
is rewritten in place; unrelated lines retain their original order and text.

### Save failure is visible and retry-safe

The save error is now shown on the verification screen, which is the screen
reached after a successful connection. This is where a non-fatal `.env` write
failure is actually observable by the operator.

Starting a new connection attempt clears both the previous connection error and
the previous save error, preventing stale feedback from appearing on a retry.

### Screen 1 layout

The editable rows now appear together in tab order:

```text
Server
Login
Password
Database
Auth
Encrypted
```

The save checkbox remains below the connection details as the fourth control.

## Test coverage added

- `UpdateDotEnv` preserves an existing POSIX mode (`0640`); skipped on Windows,
  where Go permission bits do not describe NTFS ACLs.
- Typing while the save checkbox has focus changes no text field, while Space
  toggles the checkbox.
- A refused connection test now covers both a normal failure (server field
  focused) and a wrapped `ErrNoPassword` (password field focused).
- `buildOptionsForWizard` is directly tested with no configured server; the
  equivalent command path still returns exit code 2 and `ErrNoServer`.
- A rendered verification frame includes a non-fatal `.env` save error.
- A new connection attempt clears a prior save error.

## Validation to run

```text
go test -count=100 -run TestUpdateDotEnv ./collect
go test -count=1 ./...
go vet ./...
git diff --check
```

No commit was created by this change.
