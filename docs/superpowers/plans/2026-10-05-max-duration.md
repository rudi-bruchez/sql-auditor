# Bounding the duration of a collection: implementation plan

> For agentic workers: REQUIRED SUB-SKILL: use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task by task, together with `~/.claude/skills/subagent-implementation/SKILL.md`, whose requirements every brief below carries. Steps use checkbox (`- [ ]`) syntax for tracking.

Goal: `--max-duration D` / `MAX_DURATION` bounds a whole `collect` run: once the bound passes, no collector starts, the one running is stopped, every unit not started is recorded as an omission, and the run exits 2 with a partial archive.

Architecture: one context, `bound`, made by `Run` from `started` with `context.WithDeadlineCause(..., errMaxDurationReached)`. Whether the bound cut the run is a fact read from a context's first cause at three moments (a step before the run folder, a skip before a unit, a failing server call inside a unit), written once to `run.max_duration_reached`, and every consumer (exit code, `MANIFEST.txt`, the note, the wizard) reads that field. What an error says is a separate question, answered per call for its words only.

Tech stack: Go standard library, go-mssqldb 1.10.0 (already a dependency), no new dependency.

Spec: `docs/max-duration-spec.md`, version 6 of 5 October 2026, with the owner's rulings recorded just above "Review of 4 October 2026" and in "Open questions". Executors read the spec's section named in each task before starting it. Where this plan and the spec disagree, the spec wins and the implementer stops and says so.

Evidence: a reviewer's prototype of versions 5 and 6, with mutations switched by `MAXDUR_MUT`, lives in `/tmp/claude-1000/-home-rudi-Sources-Repos-sql-auditor-workspace/8d1970c4-e022-43df-949e-caf595554eee/scratchpad/maxdur6/tree` (`collect/maxdur.go`, `collect/zz_v6_live_test.go`, `collect/zz_v6_check_test.go`). It shows the change is feasible and several tests below are adapted from it. It deviates from the spec in two places this plan does not follow: it records the verdict in a package variable instead of passing it through `Observer.Finished`, and it has no `summaryTail`. The `/tmp` copy may be gone by execution time; nothing below depends on it.

## Global constraints

- The repository is public (`CLAUDE.md`): no client instance, host, database, login or IP in code, tests, docs or commit messages. Invented names only: `SQL01`, `SALESDB`, `HRDB`, `OPSDB`, `192.0.2.0/24`.
- Commit messages in English, prose explaining why, no bullet list, no `Co-Authored-By`, `Claude-Session`, `Generated with` or any other trailer. Stage by explicit path.
- `gofmt -l .` empty, `go vet ./...` clean, `go test ./... -count=1` green before every commit. CI also runs `go test ./collect/ -count=2` and cross-compiles for linux and windows amd64.
- Standard library only. No new module.
- Format of the value: a Go duration, whole seconds, at least one minute. Refusal message, verbatim: `MAX_DURATION: invalid value "30s", want a whole number of seconds of at least 1m, such as 90m or 2h` (with the typed value in place of `30s`). The floor lives in `Resolve` only; `Run` accepts any positive `Config.MaxDuration`.
- `Config.MaxDuration` is the bound's only home. There is no `Options.MaxDuration`.
- Every sentence naming the bound is built on `maxDurationText(limit)`, which returns `the collection reached its maximum duration of ` followed by `formatCeiling(limit)` (`2h00m (7200 s)`, `0m02s (2 s)`, `1m00s (60 s)`).
- The run's context `ctx` is never replaced by `bound`. `stopRequested`, `recordUnitFailure`, the parent test of `outOfTime` and `maxDurationOr`, `leave` and the blocking watch stay on `ctx`.
- The bound's fact is read through `boundReached`, which reads the cause of `settled(bound)`, never through `context.Cause(bound)` or `bound.Err()` directly: a dial cut by the bound's deadline returns before the bound's timer has set the cause (Task 6, spec "The bound as a fact").
- A context's cause is read before that context's cancel function is called: a cancel sets the cause to `context.Canceled` when none was set, so reading after `ucancel()` or `dcancel()` would erase a deadline's cause.
- Live tests: the lab's SQL Server 2025 at `localhost,11533`, login `sa`, through `liveConfig` (`collect/watch_live_test.go`), which reads `SQL_AUDITOR_LIVE_SERVER`, `SQL_AUDITOR_LIVE_USER`, `SQL_AUDITOR_LIVE_PASSWORD` and skips when the first is unset. Every live run sets `Config.Database` to `master` (an instance-scope unit run with an empty one fails with 911 on the lab, the v5 reviser's note). No test in this plan creates a database. If one ever must, it is named `ZzMaxDur…` and dropped in a `t.Cleanup`. The existing live tests that create `ZzWatchLive` or `ZzDroppedDuringRun` are not run by any implementer; the controller runs them once, at the end of the branch ("Closing the branch").
- Tests that set `pauseHook` live in package `collect`, reset it with `defer func() { pauseHook = nil }()`, never call `t.Parallel()`, and always run under a non-zero bound (a hook that waits on `bound.Done()` with no bound waits for ever).
- The lab password is never written into the repository, this plan, a log kept in the repository, or a commit. Each live invocation below reads it with `$(LAB_SA_PASSWORD_COMMAND)`, which the controller replaces, in the brief it hands out, with the command that prints the lab's sa password on this machine.

## Review focus

The five inputs or conditions the spec implies and its criteria do not exercise, most likely first, with the test that now pins each and the task that owns it:

1. An empty `MAX_DURATION=` in `.env` beside an exported `MAX_DURATION=1h`: the operator expects the environment's hour, as for every other key. Pinned in Task 1, `TestResolveMaxDurationPrecedence`, row "an empty .env value lets the environment through".
2. `--max-duration 30s` on the command line: the operator expects exit 2 and the same sentence as for a `.env` value, not a flag-package error. Pinned in Task 2, `TestAnInvalidMaxDurationFlagIsRefusedInTheSettingsWords`.
3. A statement slow to cancel, whose driver error reads `Invalid TDS stream: did not get cancellation confirmation from the server (current response: context deadline exceeded)` rather than a bare `context deadline exceeded`: the message must still name the bound and keep the driver's words. Pinned in Task 6, `TestMaxDurationOrNamesTheBoundOnlyWhenTheCallsFirstCauseIsTheBound`, row "a driver error of other words after the bound".
4. A run with partial units that the bound cut and the operator then stopped: the summary line scripts parse must carry all three tokens in a fixed order. The function is pinned in Task 9, `TestSummaryTailOrdersTheBoundBeforeTheStop`, row "partial units, the bound and a stop"; its call site in `Run`, which prints to stdout, has no test (Task 9's break of the propagation is predicted green), and the review reads it.
5. A very long bound, `720h`, which the spec allows ("There is no upper limit"): accepted by `Resolve`, and `check` says the ceiling is under it. Pinned in Task 1 (`TestResolveReadsMaxDuration`, row `720h`) and Task 22 (`TestDurationBoundAgainstTheBound`, row "a bound far above the ceiling").

## How every task is run

This section is part of every task's brief. The controller pastes it into each implementer prompt with the task.

1. Read the spec section the task names, then the code the task touches, in the tree, before writing anything. The identifiers below were read from the tree on 5 October 2026 at `aa68236`; if one has moved or been renamed, use the tree's and say so in the report.
2. Write the test first, run it, and see it fail for the reason the step names. A test that passes before the code exists is a test that checks nothing: stop and report it.
3. Setup and tests run in one shell invocation. Shell state does not survive between tool calls: an `export` in one call and `go test` in the next runs the live tests without a server, and they print `SKIP`, which this plan counts as a failure of the step. The commands below are complete; run them as written, after setting `WT` inside the same command to the worktree you were given (for example `WT=/path/to/worktree && cd "$WT" && ...`).
4. The Bash hook of this machine rewrites `go test` into a compacting form that hides the `--- PASS` lines a count needs. Every command below goes through `rtk proxy go test`, which runs the raw command.
5. Count. Each test step gives an anchored `-run` filter and the exact number of top-level tests it must report as passed. Any other number, higher included, means the filter or the work is wrong: a higher count means the filter matched a test you did not write. `--- SKIP` on a live test means the environment was not set in that invocation.
6. Break steps are mandatory. Each one names a change to make to the new code, the test that must fail, and why. Make the change by editing, run the given command, record which tests failed, then undo the edit by editing and rerun to green. Do not use `git checkout` or `git restore` on a file that holds uncommitted work: one implementer lost a whole implementation that way. Reporting "two of three breaks failed as predicted, the third stayed green" is a successful outcome of the step, not a failure to deliver: it means an assertion checks nothing, and saying so is the most useful thing a report can contain. A break the plan predicts to stay green is listed as such.
7. If your own measurement contradicts the brief, stop and say so in your report rather than making the code match the brief.
8. When a task moves a behaviour (a check from one function to another, a call from one context to another), deleting the test that watched it at the old address is right only once the new address has one. The task says where; if it does not, say so.
9. Before committing: `gofmt -l .` prints nothing, `go vet ./...` is clean, `rtk proxy go test ./... -count=1` passes. Commit with the message given, by explicit path.

Two command forms are used. A non-live run, with the package path and filter of the step:

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^(TestA|TestB)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

A live run, the same with the three variables set in the same command:

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveA|TestLiveB)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

When a live test fails, `grep -E '^\s+\S+_test.go:[0-9]+' "$LOG"` shows the assertion lines; the tests log the run's code, flags, errors, skips, warnings and progress with `t.Logf`.

## File structure

| File | Responsibility | Tasks |
| --- | --- | --- |
| `collect/config.go` | `MAX_DURATION` key, parsing, floor, provenance | 1 |
| `cmd/sql-auditor/main.go` | `--max-duration` flag, `flags` map entry, usage line | 2 |
| `.env.example` | the commented `# MAX_DURATION=2h` | 3 |
| `collect/maxduration.go` (new) | `errMaxDurationReached`, `settled`, `boundReached`, `maxDurationText`, `maxDurationSkipReason`, `maxDurationOr`, `skipBefore`, `maxDurationNote`, `pauseHook`, `pause` | 6, 7, 9, 12 |
| `collect/observer.go` | costly units last in `planUnits`; `Verdict`; `Observer.Finished(v Verdict)` | 5, 11 |
| `collect/manifest.go` | `RunInfo.MaxDurationReached`, duration line, grouped skips, run settings paragraph, `recordedBound` | 4, 8 |
| `collect/collect.go` | `runConfig`, `outOfTime`, `summaryTail`, `settleRun`, `skipLoses` comment, `Run`, `runUnit`, `Check` | 4, 6, 9, 10, 11 to 18, 22 |
| `collect/watch.go` | `UnitSkipped.MaxDuration`, `watchOffNotice`, `recordWatchOutcome` | 9, 14, 18 |
| `collect/duration.go` | `BoundLine`, `DurationBound.Against` | 22 |
| `collect/maxduration_test.go` (new) | the helpers' tables, criteria 3, 4, 13 (note), 16b | 6, 7, 9 |
| `collect/maxduration_live_test.go` (new) | live harness and criteria 5a to 5d, 6a to 6d, 7, 16a | 11 to 17 |
| `cmd/sql-auditor/progress.go` | the gauge counts the bound's skips silently | 11, 19 |
| `tui/observer.go`, `tui/state.go`, `tui/render.go`, `tui/run.go`, `tui/loop.go` | verdict in `State`, `CollectedUnits`, silent skips, exit rule, last screen, screen 3 | 11, 20, 21, 22 |
| `README.md`, `docs/dba-guide.md`, `CHANGELOG.md` | documentation | 23 |
| `.github/workflows/ci.yml` | live max-duration tests in the integration job | 24 |
| `collect/runner.go` | `Connect` reads its caller's context through `settled` | 12 |

## Task order

Every commit compiles and leaves the suite green. Pure helpers come first and are wired later; a helper that is defined before its caller is legal Go (unused functions compile; only unused imports and locals do not). The run-level fact is introduced in the order a run meets it: the setting, the record, the verdict, the steps before the run folder, the loop's skips, the watch, the units, the hook inside a unit. The screens follow.

1. `MAX_DURATION` in `Resolve`.
2. The `--max-duration` flag.
3. The key in `.env.example`.
4. The setting in `_run.json` and `MANIFEST.txt`.
5. The order of the costly collectors.
6. The bound's words: `maxDurationOr`, `outOfTime`.
7. `skipBefore`.
8. The record of a bounded run.
9. The note, the summary tail and the watch's notice.
10. The previous run of the day.
11. The `Verdict` of `Observer.Finished`.
12. The bound's context and the steps before the run folder.
13. The check before `lockRun`.
14. The loop after the bound.
15. The check before the blocking watch.
16. Units under the bound: `cut`.
17. The hook after a failing call.
18. The watch's record after a bound stop.
19. The command line's gauge.
20. The wizard's counts.
21. The wizard's exit and last screen.
22. The bound in `check` and on the wizard's third screen.
23. Documentation.
24. The live max-duration tests in CI (ruled for by the owner).

---

### Task 1: `MAX_DURATION` in `Resolve`

Spec: "The option and its format". Criterion 1, the `Resolve` half.

Files:
- Modify: `collect/config.go` (`Config` struct after `ServerFrom`, `knownKeys`, `flagNameFor`, `Resolve`)
- Test: `collect/config_test.go`

Interfaces:
- Produces: `Config.MaxDuration time.Duration` (zero when unset), `Config.MaxDurationFrom string` (`"--max-duration"`, `".env"`, `"the environment"`, or `""` when unset); `flagNameFor("MAX_DURATION") == "max-duration"`.

- [ ] Step 1: write the failing tests. Add to `collect/config_test.go` (add `fmt` to its imports if absent):

```go
// MAX_DURATION is a Go duration in whole seconds, at least one minute. Every
// spelling of the same span resolves to the same bound, and the provenance is
// recorded, since a .env beats an exported variable here.
func TestResolveReadsMaxDuration(t *testing.T) {
	noenv := func(string) string { return "" }
	for _, c := range []struct {
		raw  string
		want time.Duration
	}{
		{"90m", 5400 * time.Second},
		{"2h", 7200 * time.Second},
		{"1h30m", 5400 * time.Second},
		{"1.5h", 5400 * time.Second},
		{"5400s", 5400 * time.Second},
		{"+2h", 7200 * time.Second},
		{"1m", time.Minute},
		{"720h", 720 * time.Hour},
	} {
		cfg, err := Resolve(nil, map[string]string{"SQL_SERVER": "SQL01", "MAX_DURATION": c.raw}, noenv)
		if err != nil {
			t.Errorf("MAX_DURATION=%s: %v", c.raw, err)
			continue
		}
		if cfg.MaxDuration != c.want || cfg.MaxDurationFrom != ".env" {
			t.Errorf("MAX_DURATION=%s: got %s from %q, want %s from .env", c.raw, cfg.MaxDuration, cfg.MaxDurationFrom, c.want)
		}
	}
	cfg, err := Resolve(nil, map[string]string{"SQL_SERVER": "SQL01"}, noenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxDuration != 0 || cfg.MaxDurationFrom != "" {
		t.Errorf("unset: got %s from %q, want no bound and no provenance", cfg.MaxDuration, cfg.MaxDurationFrom)
	}
}

// A slip of unit (30s for 30m), a bare number (7200 read as seconds or as
// hours?), a fraction of a second the record would truncate: each is refused
// in one sentence naming the key.
func TestResolveRefusesAMaxDurationItCannotHonour(t *testing.T) {
	noenv := func(string) string { return "" }
	for _, raw := range []string{"30s", "0", "-5m", "1m0.5s", "59.5s", "7200", "two hours"} {
		_, err := Resolve(nil, map[string]string{"SQL_SERVER": "SQL01", "MAX_DURATION": raw}, noenv)
		want := fmt.Sprintf("MAX_DURATION: invalid value %q, want a whole number of seconds of at least 1m, such as 90m or 2h", raw)
		if err == nil || err.Error() != want {
			t.Errorf("MAX_DURATION=%s: err = %v, want %q", raw, err, want)
		}
	}
}

// The usual precedence: the flag, then .env, then the environment. An empty
// value in .env means unset at that level and lets the environment through.
func TestResolveMaxDurationPrecedence(t *testing.T) {
	env := func(k string) string {
		if k == "MAX_DURATION" {
			return "1h"
		}
		return ""
	}
	for _, c := range []struct {
		name   string
		flags  map[string]string
		dotenv map[string]string
		want   time.Duration
		from   string
	}{
		{"the flag beats .env", map[string]string{"MAX_DURATION": "3h"},
			map[string]string{"SQL_SERVER": "SQL01", "MAX_DURATION": "2h"}, 3 * time.Hour, "--max-duration"},
		{".env beats the environment", nil,
			map[string]string{"SQL_SERVER": "SQL01", "MAX_DURATION": "2h"}, 2 * time.Hour, ".env"},
		{"the environment alone", nil,
			map[string]string{"SQL_SERVER": "SQL01"}, time.Hour, "the environment"},
		{"an empty .env value lets the environment through", nil,
			map[string]string{"SQL_SERVER": "SQL01", "MAX_DURATION": ""}, time.Hour, "the environment"},
	} {
		cfg, err := Resolve(c.flags, c.dotenv, env)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if cfg.MaxDuration != c.want || cfg.MaxDurationFrom != c.from {
			t.Errorf("%s: got %s from %q, want %s from %q", c.name, cfg.MaxDuration, cfg.MaxDurationFrom, c.want, c.from)
		}
	}
}
```

- [ ] Step 2: run and see them fail. `Config` has no `MaxDuration` field, so the package does not compile: expected `go test exit: 1` with `undefined` or `unknown field` errors naming `MaxDuration` in the log (`grep -n MaxDuration "$LOG"`).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^TestResolve(ReadsMaxDuration|RefusesAMaxDurationItCannotHonour|MaxDurationPrecedence)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |MaxDuration' "$LOG"
```

- [ ] Step 3: implement. In `Config`, right after `ServerFrom string`:

```go
	// MaxDuration bounds the whole collection, counted from the moment Run
	// takes started; zero means no bound. It is the bound's only home: Run
	// reads it here, and so do check and the wizard, so a path that built its
	// Options without copying a second field cannot print a bound it does not
	// apply. The one-minute floor is Resolve's; Run takes any positive value,
	// which is what lets a test bound a run by two seconds.
	MaxDuration time.Duration
	// MaxDurationFrom names where MaxDuration came from, as ServerFrom does
	// for the server, and is empty when no bound was set.
	MaxDurationFrom string
```

In `knownKeys`, add `"MAX_DURATION": true`. In `flagNameFor`, add:

```go
	case "MAX_DURATION":
		return "max-duration"
```

In `Resolve`, after `queryStoreTo := dateShapeOf("QUERY_STORE_TO")` and before `cfg := &Config{`:

```go
	// A Go duration, so that a window counted in hours is typed in hours.
	// Whole seconds, because the bound is recorded and printed in whole
	// seconds and a deadline that differs from its record is a record that
	// lies; at least a minute, against 30s typed for 30m. time.ParseDuration
	// already refuses a bare number ("missing unit in duration").
	var maxDuration time.Duration
	maxDurationFrom := ""
	if raw := get("MAX_DURATION", ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < time.Minute || d%time.Second != 0 {
			if firstErr == nil {
				firstErr = fmt.Errorf("MAX_DURATION: invalid value %q, want a whole number of seconds of at least 1m, such as 90m or 2h", raw)
			}
		} else {
			maxDuration, maxDurationFrom = d, from["MAX_DURATION"]
		}
	}
```

and in the `cfg := &Config{...}` literal, after `ServerFrom: from["SQL_SERVER"],`:

```go
		MaxDuration:     maxDuration,
		MaxDurationFrom: maxDurationFrom,
```

- [ ] Step 4: run the command of step 2. Expected: `go test exit: 0`, `top-level passes: 3`, no `FAIL`.

- [ ] Step 5: breaks. Each must make the named test fail; undo each before the next.

| Change | Test that must fail | Why |
| --- | --- | --- |
| Remove `\|\| d%time.Second != 0` | `TestResolveRefusesAMaxDurationItCannotHonour` (`1m0.5s`) | the fraction guard |
| Replace `d < time.Minute` by `d <= 0` | `TestResolveRefusesAMaxDurationItCannotHonour` (`30s`, `59.5s`) | the floor |
| Delete the `case "MAX_DURATION"` of `flagNameFor` | `TestResolveMaxDurationPrecedence` (`--MAX_DURATION`) | the provenance |
| Delete `MaxDuration: maxDuration,` from the literal | `TestResolveReadsMaxDuration` | propagation: the parsed value must reach `Config` |
| Delete `"MAX_DURATION": true` from `knownKeys` | all three (`unrecognised setting(s): MAX_DURATION`) | propagation upstream: `checkKeys` feeds `Resolve` |

- [ ] Step 6: full suite, gofmt, vet, then commit.

```bash
git add collect/config.go collect/config_test.go
git commit -m "Read MAX_DURATION as a bound on the whole collection

The setting is a Go duration because a collection window is counted in
hours, and a bare number of seconds invites the factor-of-sixty mistake.
Resolve refuses anything under a minute or with a fraction of a second,
since the bound is recorded in whole seconds and a deadline that differs
from its record would make the record lie. The value lives in Config alone,
with its provenance, so that check, the wizard and Run cannot disagree
about which bound applies."
```

### Task 2: the `--max-duration` flag

Spec: "The option and its format" (the checklist), criteria 1 (command line half) and 2.

Files:
- Modify: `cmd/sql-auditor/main.go` (`cliFlags`, `defineFlags`, the `flags` map in `optionsFrom`, the usage text after the `--keep` line)
- Test: `cmd/sql-auditor/main_test.go`

Interfaces:
- Consumes: `Config.MaxDuration`, `Config.MaxDurationFrom` (Task 1).
- Produces: `cliFlags.maxDuration string`; `flags["MAX_DURATION"]` handed to `collect.Resolve` when the flag is non-empty.

- [ ] Step 1: write the failing tests in `cmd/sql-auditor/main_test.go`. The helpers `writeDotEnv`, `noEnv`, `noStdin` and `writeUsage` exist.

```go
// The bound reaches Run through the command line's own path, not Resolve
// alone: the flag is parsed, handed to Resolve in the flags map, and lands in
// Options.Config. Without the map entry the flag is parsed and dropped.
func TestMaxDurationReachesTheRunFromTheFlagAndFromDotEnv(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\n")
	o, code, err := buildOptions("collect", []string{"--env", env, "--max-duration", "2h"}, noEnv, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.MaxDuration != 2*time.Hour || o.Config.MaxDurationFrom != "--max-duration" {
		t.Errorf("flag: got %s from %q, want 2h0m0s from --max-duration", o.Config.MaxDuration, o.Config.MaxDurationFrom)
	}
	env = writeDotEnv(t, "SQL_SERVER=invalid.invalid\nMAX_DURATION=2h\n")
	o, code, err = buildOptions("collect", []string{"--env", env}, noEnv, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.MaxDuration != 2*time.Hour || o.Config.MaxDurationFrom != ".env" {
		t.Errorf(".env: got %s from %q, want 2h0m0s from .env", o.Config.MaxDuration, o.Config.MaxDurationFrom)
	}
}

// A value typed on the command line is refused in the same words as one in
// .env, because both go through Resolve.
func TestAnInvalidMaxDurationFlagIsRefusedInTheSettingsWords(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\n")
	_, code, err := buildOptions("collect", []string{"--env", env, "--max-duration", "30s"}, noEnv, noStdin)
	want := `MAX_DURATION: invalid value "30s", want a whole number of seconds of at least 1m, such as 90m or 2h`
	if code != 2 || err == nil || err.Error() != want {
		t.Errorf("code %d, err %v; want 2 and %q", code, err, want)
	}
}

// --all turns on the eleven opt-ins and changes nothing else; a bound is not
// an opt-in. TestAllTurnsOnEveryOptIn is not this guard: a bound added to
// KnownFlags and to the options' Flags passed it.
func TestAllSetsNoMaxDuration(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\n")
	o, code, err := buildOptions("collect", []string{"--env", env, "--all"}, noEnv, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Config.MaxDuration != 0 {
		t.Errorf("--all set a bound of %s", o.Config.MaxDuration)
	}
	for _, set := range []map[string]string{collect.KnownFlags, collect.ValueFlags} {
		for k, v := range set {
			if strings.Contains(strings.ToLower(k+v), "duration") {
				t.Errorf("%s (%s) names the bound among the opt-ins --all turns on", k, v)
			}
		}
	}
}

func TestTheUsageNamesMaxDuration(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf)
	if !strings.Contains(buf.String(), "--max-duration D") {
		t.Error("the usage text does not list --max-duration")
	}
}
```

(Add `bytes` to the imports of `main_test.go` if absent; `time`, `strings` and `collect` are there.)

- [ ] Step 2: run and see them fail: `TestMaxDurationReachesTheRunFromTheFlagAndFromDotEnv` fails on the flag (`flag provided but not defined: -max-duration` exits the test binary through `flag.ExitOnError`, so expect `go test exit: 1` and an exit status 2 message in the log), `TestTheUsageNamesMaxDuration` fails.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./cmd/sql-auditor/ -run '^(TestMaxDurationReachesTheRunFromTheFlagAndFromDotEnv|TestAnInvalidMaxDurationFlagIsRefusedInTheSettingsWords|TestAllSetsNoMaxDuration|TestTheUsageNamesMaxDuration)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |max-duration' "$LOG"
```

- [ ] Step 3: implement. In `cliFlags`, add `maxDuration string` to the line with `server, user, envFile, queriesDir, outputDir string` (or on its own line). In `defineFlags`, after the `--keep` flag:

```go
	// A string, not a time.Duration flag, so that Resolve parses and refuses
	// the value in the same words whichever of the three sources it came from.
	fs.StringVar(&c.maxDuration, "max-duration", "",
		"bound the whole collection, e.g. 90m or 2h (overrides MAX_DURATION)")
```

In `optionsFrom`, in the map literal beside `"QUERIES_DIR": c.queriesDir, "OUTPUT_DIR": c.outputDir,`, add `"MAX_DURATION": c.maxDuration,`. In the usage text, after the `--keep` line:

```
  --max-duration D            bound the whole collection, counted from its
                              start: once D has passed, no collector starts,
                              the one running is stopped, and the run exits 2
                              with a partial archive. A Go duration in whole
                              seconds, at least one minute: 90m, 2h, 1h30m.
                              Overrides MAX_DURATION.
```

- [ ] Step 4: run the command of step 2. Expected: `top-level passes: 4`, no `FAIL`.

- [ ] Step 5: breaks.

| Change | Test that must fail | Why |
| --- | --- | --- |
| Delete `"MAX_DURATION": c.maxDuration,` from the map | `TestMaxDurationReachesTheRunFromTheFlagAndFromDotEnv` (flag half) | propagation: parsed and never handed to `Resolve` |
| Add `"max_duration": "--max-duration",` to `collect.KnownFlags` in `collect/queryset.go` | `TestAllSetsNoMaxDuration` (and `TestAllTurnsOnEveryOptIn`, expected too) | the bound must not become an opt-in |
| Delete the usage lines | `TestTheUsageNamesMaxDuration` | the help |

- [ ] Step 6: full suite, gofmt, vet, commit.

```bash
git add cmd/sql-auditor/main.go cmd/sql-auditor/main_test.go
git commit -m "Accept --max-duration on the command line

The flag is a string handed to Resolve beside the other settings, so that
a value typed on the command line is refused in the same sentence as one in
.env, and so that the bound reaches Run through Config only. It is not an
opt-in and --all does not set it: --all asks for the widest archive and
changes nothing else."
```

### Task 3: the key in `.env.example`

Spec: "The option and its format" (`.env.example`), criterion 1, last sentence.

Files:
- Modify: `.env.example` (new section after `# --- Timeouts (seconds) ---` and its two keys)
- Test: `queries_test.go` (package `sqlauditor_test`)

- [ ] Step 1: write the failing test in `queries_test.go`:

```go
// MAX_DURATION ships commented. An assignment, even an empty one, would make
// every .env that env init writes unreadable by 0.37.0 and older, which
// refuse a key they do not know whatever its value.
func TestEmbeddedEnvTemplateLeavesMaxDurationCommented(t *testing.T) {
	if !strings.Contains(sqlauditor.EnvExample, "\n# MAX_DURATION=2h\n") {
		t.Error("the template does not carry the commented line # MAX_DURATION=2h")
	}
	parsed, err := collect.ParseDotEnv(strings.NewReader(sqlauditor.EnvExample))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["MAX_DURATION"]; ok {
		t.Error("the template sets MAX_DURATION; it must only show it commented")
	}
}
```

- [ ] Step 2: run; it fails on the missing line.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test . -run '^(TestEmbeddedEnvTemplateLeavesMaxDurationCommented|TestEmbeddedEnvTemplateIsAcceptedByTheResolver)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected before the change: `top-level passes: 1` (the existing resolver test), one `--- FAIL`.

- [ ] Step 3: add to `.env.example`, after `SQL_QUERY_TIMEOUT_SEC=60` and its blank line:

```
# --- Duration of the collection ---
# MAX_DURATION bounds the whole collection, counted from its start: once it
# has passed, no collector starts, the one running is stopped, and the run
# exits 2 with a partial archive. A Go duration in whole seconds, at least one
# minute: 90m, 2h, 1h30m. It stays commented: 0.37.0 and older refuse a .env
# that sets it, even to nothing. Default: unset, no bound.
# MAX_DURATION=2h
```

- [ ] Step 4: run step 2's command. Expected `top-level passes: 2`. The existing `TestEmbeddedEnvTemplateIsAcceptedByTheResolver` uncomments every `# KEY=` line and resolves the result, so it now also proves `2h` is accepted (it depends on Task 1's `knownKeys` entry).

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| Uncomment the line (`MAX_DURATION=2h`) | `TestEmbeddedEnvTemplateLeavesMaxDurationCommented` |
| Write `MAX_DURATION=` instead | `TestEmbeddedEnvTemplateLeavesMaxDurationCommented` |
| Write `# MAX_DURATION=30s` | `TestEmbeddedEnvTemplateIsAcceptedByTheResolver` and the new test |

- [ ] Step 6: commit.

```bash
git add .env.example queries_test.go
git commit -m "Show MAX_DURATION in the env template, commented

The template is what env init writes, and an older binary refuses a .env
holding a key it does not know, empty or not. Shipping the key as a
commented example documents it without making every new .env unreadable by
0.37.0."
```

### Task 4: the setting in `_run.json` and `MANIFEST.txt`

Spec: "The option and its format" (checklist: `runConfig`, the run settings paragraph), "How a bounded run is recorded" (`config.max_duration_sec`), criteria 2 (last clause) and 9 (the key).

Files:
- Modify: `collect/collect.go` (`runConfig`), `collect/manifest.go` (the run settings paragraph in `Human`)
- Test: `collect/runconfig_test.go`, `collect/manifest_test.go`

Interfaces:
- Produces: `_run.json` `config.max_duration_sec`, the bound in whole seconds as a string (`"7200"`), present exactly when `Config.MaxDuration > 0`.

- [ ] Step 1: tests.

In `collect/runconfig_test.go` (add `time` to its imports):

```go
// The bound is recorded in the unit of duration_sec beside it, and only when
// one was set, so that its absence means the same thing in this version's
// manifests and in older ones.
func TestRunConfigRecordsTheMaxDurationOnlyWhenSet(t *testing.T) {
	c := runConfig(Options{Config: &Config{MaxDuration: 2 * time.Hour}})
	if c["max_duration_sec"] != "7200" {
		t.Errorf("max_duration_sec = %q, want 7200", c["max_duration_sec"])
	}
	all := map[string]bool{}
	for name := range KnownFlags {
		all[name] = true
	}
	off := runConfig(Options{Config: &Config{}, Flags: all})
	if v, ok := off["max_duration_sec"]; ok {
		t.Errorf("a run with --all and no bound records max_duration_sec = %q", v)
	}
}
```

In `collect/manifest_test.go`:

```go
func TestManifestTextNamesTheMaxDurationAmongTheRunSettings(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "")
	if !strings.Contains(m.Human(), "maximum duration the run was given when one was set") {
		t.Error("MANIFEST.txt does not say the run settings include the bound")
	}
}
```

- [ ] Step 2: run, both fail.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^(TestRunConfigRecordsTheMaxDurationOnlyWhenSet|TestManifestTextNamesTheMaxDurationAmongTheRunSettings|TestRunConfigRecordsEveryOptIn)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected before: `top-level passes: 1` (the existing `TestRunConfigRecordsEveryOptIn`).

- [ ] Step 3: implement. In `runConfig`, before `return c`:

```go
	// In whole seconds, the unit of duration_sec beside it, and absent when no
	// bound was set. Without this line every other part of the bound can be in
	// place and no manifest records it.
	if o.Config.MaxDuration > 0 {
		c["max_duration_sec"] = strconv.FormatInt(int64(o.Config.MaxDuration/time.Second), 10)
	}
```

In `collect/manifest.go`, replace the paragraph that begins `The password of the login used for this run is recorded nowhere in this` with:

```
The password of the login used for this run is recorded nowhere in this
archive. The run settings in _run.json are the query and output directories,
the database name filters, which optional collections were switched on, the
window and per-database limits the Query Store extraction was given, and the
maximum duration the run was given when one was set; any setting whose name
marks it as a password, token or other secret is replaced with "(redacted)"
before that block is written.
```

If an existing test quotes the old wording (`grep -rn "per-database limits the Query Store" collect/*_test.go`), move its quotation to the new wording.

- [ ] Step 4: rerun. Expected `top-level passes: 3`.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| Delete the `if` block in `runConfig` | `TestRunConfigRecordsTheMaxDurationOnlyWhenSet` (first assertion) |
| Write the key unconditionally (drop the `if`, keep the assignment) | `TestRunConfigRecordsTheMaxDurationOnlyWhenSet` (absence) |
| Restore the old paragraph | `TestManifestTextNamesTheMaxDurationAmongTheRunSettings` |

- [ ] Step 6: commit.

```bash
git add collect/collect.go collect/manifest.go collect/runconfig_test.go collect/manifest_test.go
git commit -m "Record the bound among the run settings

A bound forgotten in a .env must not be silent, and the manifest is where a
reader looks for what a run was given. The key is written only when a bound
was set, so that its absence means the same thing in older manifests."
```

### Task 5: the order of the costly collectors

Spec: "The order of the units". Criterion 8 and the order clause of criterion 9.

Files:
- Modify: `collect/observer.go` (`planUnits`, imports)
- Test: `collect/observer_test.go`

Interfaces:
- Produces: `planUnits` returns the units of every script whose `RequiresFlag` is in `CostFlags` after all the others, each group in the order it had before. Skips and errors are unchanged. `PlannedDuration` and `Run` both call `planUnits`, so both see this order.

- [ ] Step 1: tests in `collect/observer_test.go`:

```go
func unitLabels(units []unit) []string {
	var out []string
	for _, u := range units {
		out = append(out, u.Script.Path+"|"+u.Target.Name)
	}
	return out
}

// With a bound, what runs last is what is lost, and the costly collectors sit
// in 70.schema, before the Query Store. They move to the end of the plan, in
// every run, keeping the order within each group. The costly set is derived
// from CostFlags, never from a list of paths.
func TestPlanUnitsRunsTheCostlyCollectorsLast(t *testing.T) {
	var costly []string
	for f := range CostFlags {
		costly = append(costly, f)
	}
	slices.Sort(costly)
	if len(costly) < 2 {
		t.Fatalf("CostFlags holds %d flags; this test needs two", len(costly))
	}
	folders := []DatabaseFolder{{Name: "SALESDB", Folder: "SALESDB"}, {Name: "HRDB", Folder: "HRDB"}}
	plan := []plannedScript{
		{Script: Script{Path: "20.databases/010.a.sql", Scope: ScopeDatabase}},
		{Script: Script{Path: "70.schema/041.b.sql", Scope: ScopeDatabase, RequiresFlag: costly[0]}},
		{Script: Script{Path: "70.schema/050.c.sql", Scope: ScopeDatabase}},
		{Script: Script{Path: "70.schema/055.d.sql", Scope: ScopeDatabase, RequiresFlag: costly[1]}},
		{Script: Script{Path: "80.workload/020.e.sql"}},
		{Script: Script{Path: "90.availability/010.f.sql", RequiresFlag: FlagIncludeSessionText}},
	}
	// The expected order, derived: every unit as the plan unfolds it, the
	// ones gated by a cost flag moved after the rest.
	var cheap, dear []string
	for _, p := range plan {
		targets := []string{""}
		if p.Script.Scope == ScopeDatabase {
			targets = []string{"SALESDB", "HRDB"}
		}
		for _, db := range targets {
			l := p.Script.Path + "|" + db
			if CostFlags[p.Script.RequiresFlag] {
				dear = append(dear, l)
			} else {
				cheap = append(cheap, l)
			}
		}
	}
	units, _, _ := planUnits(plan, folders, &Config{})
	if got, want := unitLabels(units), append(cheap, dear...); !slices.Equal(got, want) {
		t.Errorf("order:\n got  %v\n want %v", got, want)
	}
}

// A plan with no cost option keeps today's order exactly: script after
// script, each over every database.
func TestPlanUnitsKeepsThePlanOrderWithoutACostlyCollector(t *testing.T) {
	folders := []DatabaseFolder{{Name: "SALESDB", Folder: "SALESDB"}, {Name: "HRDB", Folder: "HRDB"}}
	plan := []plannedScript{
		{Script: Script{Path: "20.databases/010.a.sql", Scope: ScopeDatabase}},
		{Script: Script{Path: "70.schema/050.c.sql", Scope: ScopeDatabase}},
		{Script: Script{Path: "80.workload/020.e.sql"}},
	}
	units, _, _ := planUnits(plan, folders, &Config{})
	want := []string{"20.databases/010.a.sql|SALESDB", "20.databases/010.a.sql|HRDB",
		"70.schema/050.c.sql|SALESDB", "70.schema/050.c.sql|HRDB", "80.workload/020.e.sql|"}
	if got := unitLabels(units); !slices.Equal(got, want) {
		t.Errorf("order:\n got  %v\n want %v", got, want)
	}
}
```

- [ ] Step 2: run; the first fails (the costly units are in place), the second passes.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^TestPlanUnits(RunsTheCostlyCollectorsLast|KeepsThePlanOrderWithoutACostlyCollector)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected before: `top-level passes: 1`, one `FAIL`.

- [ ] Step 3: implement. Add `"slices"` to the imports of `collect/observer.go`. In `planUnits`, replace `return units, skipped, errs` with:

```go
	// The units of a collector gated by a cost option run after all the
	// others, each group in plan order. With a bound, what runs last is what
	// is lost, and the costly collectors sit in 70.schema, ahead of the Query
	// Store and the plan cache; without one, a ctrl-c at the operator's own
	// limit keeps everything but the costly part. It is done here because
	// PlannedDuration and Run both walk this list, so the order check
	// describes is the order the run follows. A stable sort keeps a nil slice
	// nil and leaves a plan with no costly unit exactly as it was.
	slices.SortStableFunc(units, func(a, b unit) int {
		ca, cb := CostFlags[a.Script.RequiresFlag], CostFlags[b.Script.RequiresFlag]
		switch {
		case ca == cb:
			return 0
		case cb:
			return -1
		}
		return 1
	})
	return units, skipped, errs
```

- [ ] Step 4: rerun: `top-level passes: 2`. Then the whole `collect` package and `tui` (the wizard's ceiling uses `PlannedDuration`): `rtk proxy go test ./... -count=1`.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| Delete the `SortStableFunc` call | `TestPlanUnitsRunsTheCostlyCollectorsLast` |
| Swap `return -1` and `return 1` (costly first) | `TestPlanUnitsRunsTheCostlyCollectorsLast` |
| Sort every unit by path (`strings.Compare(a.Script.Path, b.Script.Path)`) | `TestPlanUnitsRunsTheCostlyCollectorsLast` (the database order within a script would hold, the groups would not) |

`slices.SortFunc` in place of the stable sort is not a required break: it may or may not reorder equal elements on a list this short.

- [ ] Step 6: commit.

```bash
git add collect/observer.go collect/observer_test.go
git commit -m "Run the costly collectors after all the others

With a bound on the collection, what runs last is what is skipped, and in
path order the two cost options sit before the Query Store and the plan
cache, the core of a performance audit. Moving their units last in
planUnits gives check and the run one order, and changes nothing for a run
without a cost option."
```

### Task 6: the bound's words

Spec: "The bound's context" (`errMaxDurationReached`, `boundReached`, `maxDurationText`), "During a unit" ("What a cut unit's error says", "Why the innermost context", `outOfTime`). Criterion 4.

Files:
- Create: `collect/maxduration.go`
- Modify: `collect/collect.go` (`outOfTime` and its two call sites in `runUnit`)
- Test: create `collect/maxduration_test.go`; modify `collect/collect_test.go` (the four `TestOutOfTime*` call sites)

Interfaces:
- Produces:
  - `var errMaxDurationReached error`
  - `func settled(ctx context.Context) context.Context` (returns `ctx` once its deadline, if passed by the clock, has closed its `Done`)
  - `func boundReached(bound context.Context) bool` (reads the cause of `settled(bound)`)
  - `func maxDurationText(limit time.Duration) string`
  - `func maxDurationOr(parent, call context.Context, limit time.Duration, err error) error`
  - `func outOfTime(parent, call context.Context, limit, bound time.Duration, knob string, err error) error` (new parameter `bound`, the configured bound, used only for the words)

- [ ] Step 1: tests. Create `collect/maxduration_test.go`:

```go
package collect

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// passedBound is a bound that has already fired, built as Run builds it.
func passedBound(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadlineCause(parent, time.Now().Add(-time.Second), errMaxDurationReached)
}

// pastDeadline is a context whose deadline has passed by the clock while its
// Done is still open. It holds open, for as long as a test needs, the instant
// between a bound's deadline and the runtime timer that cancels the bound: a
// dial bounded by that deadline fails with i/o timeout inside that instant,
// while Err() and Cause() are still nil (measured on 5 October 2026, 200 dials
// out of 200 once the deadline had passed, and 1 run in 300 of criterion 5a's
// test on the reviewer's prototype).
type pastDeadline struct {
	context.Context
	at time.Time
}

func (p pastDeadline) Deadline() (time.Time, bool) { return p.at, true }

// boundReached must not answer while the bound's deadline has passed and its
// Done is still open: it would answer false about a bound that is firing, and
// a dial the bound cut would be filed as an unreachable instance. Deterministic:
// the bound fires only after the answer has been waited for, so an answer given
// before is always the wrong one.
func TestBoundReachedWaitsForTheBoundsTimerOnceItsDeadlineHasPassed(t *testing.T) {
	inner, fire := context.WithCancelCause(context.Background())
	defer fire(nil)
	bound := pastDeadline{inner, time.Now().Add(-time.Millisecond)}
	answer := make(chan bool, 1)
	go func() { answer <- boundReached(bound) }()
	select {
	case got := <-answer:
		t.Fatalf("boundReached answered %v before the bound's Done closed", got)
	case <-time.After(100 * time.Millisecond):
	}
	fire(errMaxDurationReached)
	if !<-answer {
		t.Error("boundReached answered false once the bound fired")
	}
	if boundReached(context.Background()) {
		t.Error("a context with no deadline reads as the bound")
	}
}

func TestMaxDurationTextUsesTheCeilingsFormat(t *testing.T) {
	for limit, want := range map[time.Duration]string{
		2 * time.Hour:   "the collection reached its maximum duration of 2h00m (7200 s)",
		2 * time.Second: "the collection reached its maximum duration of 0m02s (2 s)",
		time.Minute:     "the collection reached its maximum duration of 1m00s (60 s)",
	} {
		if got := maxDurationText(limit); got != want {
			t.Errorf("maxDurationText(%s) = %q, want %q", limit, got, want)
		}
	}
}

// maxDurationOr writes the words, and only the words, in four steps: a dead
// run context, a call whose first cause is not the bound, a SQL Server error
// number, and only then the bound's sentence. Each row below is built with
// contexts in the order the case names, because the order is what the rows
// test.
func TestMaxDurationOrNamesTheBoundOnlyWhenTheCallsFirstCauseIsTheBound(t *testing.T) {
	const limit = 2 * time.Hour
	bare := context.DeadlineExceeded
	type row struct {
		name    string
		build   func() (parent, call context.Context, done func())
		err     error
		changed string // "" means err comes back unchanged
	}
	rows := []row{
		{"a stop after the bound", func() (context.Context, context.Context, func()) {
			run, stop := context.WithCancel(context.Background())
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			<-call.Done()
			stop() // the bound first, then the operator: Cause(call) stays the bound's
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, ""},
		{"the watch first", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := context.WithDeadlineCause(run, time.Now().Add(50*time.Millisecond), errMaxDurationReached)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			cancelUnit(&blockedError{})
			<-bound.Done()
			return run, call, func() { cancelCall(); cancelBound() }
		}, bare, ""},
		{"the call's own timeout, the bound an hour away", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := context.WithDeadlineCause(run, time.Now().Add(time.Hour), errMaxDurationReached)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Millisecond)
			<-call.Done()
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, ""},
		{"straddling: the call's own timeout, then the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := context.WithDeadlineCause(run, time.Now().Add(100*time.Millisecond), errMaxDurationReached)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Millisecond)
			<-call.Done()
			<-bound.Done() // the driver's wait for the cancellation
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, ""},
		{"a SQL Server error number with the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, mssql.Error{Number: 911, Message: "Database 'SALESDB' does not exist."}, ""},
		{"a bare context error after the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, bare, "stopped when the collection reached its maximum duration of 2h00m (7200 s): context deadline exceeded"},
		{"a driver error of other words after the bound", func() (context.Context, context.Context, func()) {
			run := context.Background()
			bound, cancelBound := passedBound(run)
			unit, cancelUnit := context.WithCancelCause(bound)
			call, cancelCall := context.WithTimeout(unit, time.Hour)
			return run, call, func() { cancelCall(); cancelUnit(nil); cancelBound() }
		}, errors.New("Invalid TDS stream: did not get cancellation confirmation from the server (current response: context deadline exceeded)"),
			"stopped when the collection reached its maximum duration of 2h00m (7200 s): Invalid TDS stream: did not get cancellation confirmation from the server (current response: context deadline exceeded)"},
	}
	for _, r := range rows {
		parent, call, done := r.build()
		got := maxDurationOr(parent, call, limit, r.err)
		done()
		want := r.changed
		if want == "" {
			want = r.err.Error()
		}
		if got == nil || got.Error() != want {
			t.Errorf("%s: got %v, want %q", r.name, got, want)
		}
	}
	if maxDurationOr(context.Background(), context.Background(), limit, nil) != nil {
		t.Error("a nil error came back non-nil")
	}
}

// The same orders through outOfTime, the query path's one classification:
// the bound first names the bound, the call's own @timeout first names the
// @timeout, whatever happened after.
func TestOutOfTimeTellsTheBoundFromTheUnitsOwnTimeout(t *testing.T) {
	const limit, timeout = 2 * time.Hour, 30 * time.Minute
	bare := context.DeadlineExceeded

	run := context.Background()
	bound, cancelBound := passedBound(run)
	unit, cancelUnit := context.WithCancelCause(bound)
	call, cancelCall := context.WithTimeout(unit, timeout)
	got := outOfTime(run, call, timeout, limit, "@timeout", bare)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if want := "stopped when the collection reached its maximum duration of 2h00m (7200 s): context deadline exceeded"; got.Error() != want {
		t.Errorf("the bound first: %q, want %q", got, want)
	}

	bound, cancelBound = context.WithDeadlineCause(run, time.Now().Add(100*time.Millisecond), errMaxDurationReached)
	unit, cancelUnit = context.WithCancelCause(bound)
	call, cancelCall = context.WithTimeout(unit, time.Millisecond)
	<-call.Done()
	<-bound.Done()
	got = outOfTime(run, call, timeout, limit, "@timeout", bare)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if want := "still running when @timeout of 30m0s expired: context deadline exceeded"; got.Error() != want {
		t.Errorf("own timeout, then the bound: %q, want %q", got, want)
	}

	stopped, stop := context.WithCancel(run)
	bound, cancelBound = passedBound(stopped)
	unit, cancelUnit = context.WithCancelCause(bound)
	call, cancelCall = context.WithTimeout(unit, timeout)
	stop()
	got = outOfTime(stopped, call, timeout, limit, "@timeout", bare)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if got != bare {
		t.Errorf("a stop after the bound: %v, want the error unchanged", got)
	}

	bound, cancelBound = passedBound(run)
	unit, cancelUnit = context.WithCancelCause(bound)
	call, cancelCall = context.WithTimeout(unit, timeout)
	boom := mssql.Error{Number: 1222, Message: "Lock request time out period exceeded."}
	got = outOfTime(run, call, timeout, limit, "@timeout", boom)
	cancelCall()
	cancelUnit(nil)
	cancelBound()
	if got.Error() != boom.Error() || strings.Contains(got.Error(), "stopped when") {
		t.Errorf("a SQL error with the bound: %v, want it unchanged", got)
	}
}
```

Check the import path of the driver in `collect/collect_test.go` (`grep -n go-mssqldb collect/collect_test.go`) and use the same alias.

In `collect/collect_test.go`, the four existing calls `outOfTime(parent, unit, X, "@timeout", err)` become `outOfTime(parent, unit, X, 0, "@timeout", err)`.

- [ ] Step 2: run; the package does not compile (`undefined: errMaxDurationReached`, `too many arguments in call to outOfTime`). Expected `go test exit: 1`.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^(TestBoundReachedWaitsForTheBoundsTimerOnceItsDeadlineHasPassed|TestMaxDurationTextUsesTheCeilingsFormat|TestMaxDurationOrNamesTheBoundOnlyWhenTheCallsFirstCauseIsTheBound|TestOutOfTime.*)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined|arguments' "$LOG"
```

- [ ] Step 3: implement. Create `collect/maxduration.go`:

```go
package collect

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// errMaxDurationReached is the cause the bound's context is cancelled with.
// Every question this package asks about the bound is whether a context's
// first cause is this value: a context keeps the first cause it was cancelled
// with, so the answer does not change when the operator stops the run after
// the bound fired, and is not the bound's when the stop came first.
var errMaxDurationReached = errors.New("the collection reached its maximum duration")

// settled returns ctx once its state can be read. A context whose deadline
// has passed by the clock is cancelled an instant later, by a runtime timer,
// and a dial bounded by that deadline does not wait for it: net.Dialer turns
// the deadline into a socket deadline and returns i/o timeout, which
// errors.Is(err, context.DeadlineExceeded), while ctx.Err() and
// context.Cause(ctx) are still nil. Read in that instant, a Connect the bound
// cut reads as an unreachable instance (exit 1, no flag; measured on
// 5 October 2026). So the fact is read only once Done is closed when the
// deadline has passed. The wait is the timer's lag, microseconds; a context
// with no deadline, or one not yet passed, is returned at once.
func settled(ctx context.Context) context.Context {
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		<-ctx.Done()
	}
	return ctx
}

// boundReached says whether the bound fired before anything else cancelled
// bound. The cause and not bound.Err(): a ctrl-c cancels bound too, with the
// cause context.Canceled. Through settled, since every caller asks right after
// a step returned an error, and a failed dial returns before the bound's timer
// has run.
func boundReached(bound context.Context) bool {
	return context.Cause(settled(bound)) == errMaxDurationReached
}

// maxDurationText is the one sentence every message of this feature is built
// on. The cause is a constant and the deadline an instant, so neither carries
// the duration the operator set, and the caller passes it.
func maxDurationText(limit time.Duration) string {
	return "the collection reached its maximum duration of " + formatCeiling(limit)
}

// maxDurationOr writes the words of a failed server call, and only the words:
// whether the bound cut the run is decided elsewhere, from the call's cause,
// and never from what this returns.
//
// The order is the rule. A dead run context is the operator's stop, which
// recordUnitFailure files and drops. A call whose first cause is not the
// bound was stopped by something else first (its own deadline, the blocking
// watch), and its error keeps its words even when the bound passed during the
// driver's wait for the cancellation. A SQL Server error number is a failure
// of the server's own, as outOfTime already rules for its deadline. call is
// the context the failing call ran on, the innermost one, never the unit's.
func maxDurationOr(parent, call context.Context, limit time.Duration, err error) error {
	if err == nil || parent.Err() != nil {
		return err
	}
	if context.Cause(call) != errMaxDurationReached {
		return err
	}
	if sqlErrorNumber(err) != 0 {
		return err
	}
	return fmt.Errorf("stopped when %s: %w", maxDurationText(limit), err)
}
```

In `collect/collect.go`, replace `outOfTime` (keep its doc comment and add the paragraph below to it):

```go
// The bound's expiry reaches here too: a query context under a bound that
// fired has Err() DeadlineExceeded, so the guard lets both through, and the
// call's first cause, asked after the error number, tells the bound from the
// unit's own @timeout. bound is the configured bound, for the words.
func outOfTime(parent, call context.Context, limit, bound time.Duration, knob string, err error) error {
	if parent.Err() != nil || call.Err() != context.DeadlineExceeded {
		return err
	}
	// (the existing comment on the SQL Server error number stays here)
	if sqlErrorNumber(err) != 0 {
		return err
	}
	if context.Cause(call) == errMaxDurationReached {
		return fmt.Errorf("stopped when %s: %w", maxDurationText(bound), err)
	}
	return fmt.Errorf("still running when %s of %s expired: %w", knob, limit, err)
}
```

In `runUnit`, both `outOfTime(ctx, qctx, timeout, knob, err)` become `outOfTime(ctx, qctx, timeout, o.Config.MaxDuration, knob, err)`. Nothing runs under a bound yet, so behaviour is unchanged.

- [ ] Step 4: rerun. Expected `top-level passes: 8`: the four new tests (`TestBoundReachedWaitsForTheBoundsTimerOnceItsDeadlineHasPassed`, `TestMaxDurationTextUsesTheCeilingsFormat`, `TestMaxDurationOrNamesTheBoundOnlyWhenTheCallsFirstCauseIsTheBound`, `TestOutOfTimeTellsTheBoundFromTheUnitsOwnTimeout`) and the four existing ones the `TestOutOfTime.*` pattern also matches (`TestOutOfTimeNamesTheLimitThatExpired`, `TestOutOfTimeLeavesACancelledRunAlone`, `TestOutOfTimeLeavesASQLErrorAloneEvenOnTheDeadline`, `TestOutOfTimeLeavesAnOrdinaryFailureAlone`). If the log shows another `TestOutOfTime` name, the count is wrong: recount against the tree.

- [ ] Step 5: breaks (criterion 4's "Fails when").

| Change | Test that must fail | Row |
| --- | --- | --- |
| In `boundReached`, read `context.Cause(bound)` without `settled` | `TestBoundReachedWaitsForTheBoundsTimerOnceItsDeadlineHasPassed` (`answered false before the bound's Done closed`), every run: the bound fires only after the test has waited for the answer | |
| In `maxDurationOr`, ask the cause first and return the sentence on it, whatever the parent: split the first test into `if err == nil { return err }` and `if parent.Err() != nil { return err }`, and insert between them `if context.Cause(call) == errMaxDurationReached && sqlErrorNumber(err) == 0 { return fmt.Errorf("stopped when %s: %w", maxDurationText(limit), err) }`. Swapping the two `return err` guards alone changes nothing, since each returns `err` unchanged: that is not this break | `TestMaxDurationOrNamesTheBoundOnlyWhenTheCallsFirstCauseIsTheBound` | "a stop after the bound" |
| Move the SQL number test after the sentence's return | same | "a SQL Server error number with the bound" |
| In the test, for the straddling row only, return `unit` instead of `call` as the call context (a caller that asks the unit's context) | same | the straddling row: `Cause(unit)` is the bound's, propagated after the fact |
| Restore today's body of `outOfTime` (no cause test) | `TestOutOfTimeTellsTheBoundFromTheUnitsOwnTimeout` | the bound's row names `@timeout` |
| In `outOfTime`, move the cause test before the number test | `TestOutOfTimeTellsTheBoundFromTheUnitsOwnTimeout` | the SQL error row |
| Build `maxDurationText` with `limit.String()` | `TestMaxDurationTextUsesTheCeilingsFormat` | |

- [ ] Step 6: full suite, commit.

```bash
git add collect/maxduration.go collect/maxduration_test.go collect/collect.go collect/collect_test.go
git commit -m "Name the bound in the words of a call it stopped

The message of a failed call says which limit stopped it. Once a bound
exists, a query can be stopped by the bound, by its own @timeout, by the
blocking watch or by the operator, and the driver says context deadline
exceeded for most of them. The call's own context keeps the first cause,
so asking it, after the run's context and after the SQL Server error
number, gives each case its own sentence. Whether the bound fired is read
only once its context has settled: a dial cut by its deadline returns
before the timer that sets the cause has run."
```

### Task 7: `skipBefore`

Spec: "Before each unit". Criterion 3.

Files:
- Modify: `collect/maxduration.go`
- Test: `collect/maxduration_test.go`

Interfaces:
- Consumes: `heldBack(cancelledOn map[string]string, db string) (string, bool)` (`collect/watch.go`), `droppedBefore(on map[string]bool, db string) (string, bool)` and `skipDroppedDuringRun` (`collect/dropped.go`), `boundReached`, `maxDurationText`.
- Produces: `func maxDurationSkipReason(limit time.Duration) string`; `func skipBefore(cancelledOn map[string]string, droppedOn map[string]bool, bound context.Context, limit time.Duration, target string) (reason string, byBound, skip bool)`.

- [ ] Step 1: test in `collect/maxduration_test.go`:

```go
// The watch's and the drop's reasons are more specific than the bound's, and
// scopeLost matches on them, so they are asked first. byBound says which
// question answered, never whether the bound has passed.
func TestSkipBeforeAsksTheBoundLast(t *testing.T) {
	passed, cancelPassed := passedBound(context.Background())
	defer cancelPassed()
	far, cancelFar := context.WithDeadlineCause(context.Background(), time.Now().Add(time.Hour), errMaxDurationReached)
	defer cancelFar()
	stopped, stop := context.WithCancel(context.Background())
	stop()
	cancelledOn := map[string]string{"SALESDB": "70.schema/055.page-density.sql"}
	droppedOn := map[string]bool{"HRDB": true}
	watchReason := "the blocking watch cancelled 70.schema/055.page-density.sql on this database"
	for _, c := range []struct {
		name    string
		bound   context.Context
		limit   time.Duration
		target  string
		reason  string
		byBound bool
		skip    bool
	}{
		{"held back, bound passed", passed, 2 * time.Hour, "SALESDB", watchReason, false, true},
		{"held back, bound not passed", far, 2 * time.Hour, "SALESDB", watchReason, false, true},
		{"dropped, bound passed", passed, 2 * time.Hour, "HRDB", skipDroppedDuringRun, false, true},
		{"dropped, bound not passed", far, 2 * time.Hour, "HRDB", skipDroppedDuringRun, false, true},
		{"another database, bound passed", passed, 2 * time.Hour, "OPSDB",
			"the collection reached its maximum duration of 2h00m (7200 s) before this collector started", true, true},
		{"an instance unit, bound passed", passed, 90 * time.Minute, "",
			"the collection reached its maximum duration of 1h30m (5400 s) before this collector started", true, true},
		{"another database, bound not passed", far, 2 * time.Hour, "OPSDB", "", false, false},
		{"another database, a ctrl-c", stopped, 2 * time.Hour, "OPSDB", "", false, false},
	} {
		reason, byBound, skip := skipBefore(cancelledOn, droppedOn, c.bound, c.limit, c.target)
		if reason != c.reason || byBound != c.byBound || skip != c.skip {
			t.Errorf("%s: got (%q, %v, %v), want (%q, %v, %v)", c.name, reason, byBound, skip, c.reason, c.byBound, c.skip)
		}
	}
}
```

- [ ] Step 2: run; does not compile (`undefined: skipBefore`).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^TestSkipBeforeAsksTheBoundLast$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined' "$LOG"
```

- [ ] Step 3: implement in `collect/maxduration.go`:

```go
// maxDurationSkipReason is the reason of every unit the bound kept from
// starting: one string per run, because MANIFEST.txt groups on it.
func maxDurationSkipReason(limit time.Duration) string {
	return maxDurationText(limit) + " before this collector started"
}

// skipBefore is the loop's three questions before a unit, in their order: a
// database where the watch cancelled a collector, a database found dropped,
// then the bound. byBound says the bound answered; asking boundReached again
// after this returns would claim a held-back unit for the bound once the
// bound has passed, and count it in the note while MANIFEST.txt files it
// under its own reason.
func skipBefore(cancelledOn map[string]string, droppedOn map[string]bool, bound context.Context, limit time.Duration, target string) (reason string, byBound, skip bool) {
	if r, ok := heldBack(cancelledOn, target); ok {
		return r, false, true
	}
	if r, ok := droppedBefore(droppedOn, target); ok {
		return r, false, true
	}
	if boundReached(bound) {
		return maxDurationSkipReason(limit), true, true
	}
	return "", false, false
}
```

- [ ] Step 4: rerun: `top-level passes: 1`.

- [ ] Step 5: breaks.

| Change | Row that must fail |
| --- | --- |
| Ask the bound first | "held back, bound passed" gets the bound's reason |
| Return `boundReached(bound)` as `byBound` in the two first branches | "held back, bound passed", "dropped, bound passed" |
| Build the reason from `2 * time.Hour` instead of `limit` | "an instance unit, bound passed" |
| `boundReached` replaced by `bound.Err() != nil` | "another database, a ctrl-c" |

- [ ] Step 6: commit.

```bash
git add collect/maxduration.go collect/maxduration_test.go
git commit -m "Decide in one function why a unit is not started

The loop already skips a unit on a database the watch held back or found
dropped; the bound is a third reason, asked last because the other two are
more specific and the rerun guard matches on them. Returning which question
answered keeps the bound from claiming a skip it did not cause."
```

### Task 8: the record of a bounded run

Spec: "How a bounded run is recorded" (`run.max_duration_reached`, the duration line, "Queries not run"). Criterion 9, the manifest's half.

Files:
- Modify: `collect/manifest.go` (`RunInfo`, `Human`'s duration line, `writeNotRun`, new `recordedBound`)
- Test: `collect/manifest_test.go`

Interfaces:
- Produces: `RunInfo.MaxDurationReached bool` with JSON `max_duration_reached,omitempty`; `func recordedBound(cfg map[string]string) (time.Duration, bool)`.

- [ ] Step 1: tests in `collect/manifest_test.go` (add `fmt` and `time` to its imports: at `aa68236` it imports `encoding/json` but neither of these, and `TestManifestTextGroupsTheBoundsSkips` uses `fmt.Sprintf`):

```go
// A reader of MANIFEST.txt alone has no other way to learn the run was cut,
// so the duration line says it, and says it even for a run cut in its first
// second, which only a test can produce.
func TestManifestTextSaysTheRunStoppedAtTheMaxDuration(t *testing.T) {
	for _, c := range []struct {
		sec  int
		want string
	}{
		{7204, "Duration     : 7204 s, stopped at the maximum duration of 2h00m (7200 s)\n"},
		{0, "Duration     : 0 s, stopped at the maximum duration of 2h00m (7200 s)\n"},
	} {
		m := NewManifest("sql-auditor", "test", "")
		m.Config = map[string]string{"max_duration_sec": "7200"}
		m.Run.DurationSec, m.Run.MaxDurationReached = c.sec, true
		if h := m.Human(); !strings.Contains(h, c.want) {
			t.Errorf("duration %d: no line %q in\n%s", c.sec, c.want, h)
		}
	}
}

// A bound that fires early skips hundreds of units for one reason. They are
// one entry, written where the first of them falls, as a dropped database's
// are.
func TestManifestTextGroupsTheBoundsSkips(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "")
	m.Config = map[string]string{"max_duration_sec": "7200"}
	reason := maxDurationSkipReason(2 * time.Hour)
	for i := 0; i < 12; i++ {
		m.Skipped = append(m.Skipped, SkippedScript{Script: fmt.Sprintf("10.system/%03d.x.sql", i), Reason: "requires SQL Server 2016 or later"})
	}
	for i := 0; i < 198; i++ {
		m.Skipped = append(m.Skipped, SkippedScript{Script: fmt.Sprintf("80.workload/%03d.y.sql", i), Target: "SALESDB", Reason: reason})
	}
	h := m.Human()
	for _, want := range []string{
		"Queries not run (210):\n",
		"  - 198 collectors not started, each listed in _run.json\n      " + reason + "\n",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("no %q in\n%s", want, h)
		}
	}
	if n := strings.Count(h, reason); n != 1 {
		t.Errorf("the bound's reason appears %d times, want once", n)
	}
}

// Without a bound nothing changes: today's duration line, and no
// max_duration_reached in _run.json.
func TestManifestWithoutABoundKeepsTodaysDurationLine(t *testing.T) {
	m := NewManifest("sql-auditor", "test", "")
	m.Run.DurationSec = 108
	if h := m.Human(); !strings.Contains(h, "Duration     : 108 s\n") || strings.Contains(h, "maximum duration of") {
		t.Errorf("duration line changed without a bound:\n%s", h)
	}
	b, err := json.Marshal(m.Run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "max_duration_reached") {
		t.Errorf("_run.json carries max_duration_reached on an unbounded run: %s", b)
	}
}
```

- [ ] Step 2: run; does not compile (`m.Run.MaxDurationReached undefined`).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^TestManifest(TextSaysTheRunStoppedAtTheMaxDuration|TextGroupsTheBoundsSkips|WithoutABoundKeepsTodaysDurationLine)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined' "$LOG"
```

- [ ] Step 3: implement. In `RunInfo`, after `Cancelled`:

```go
	// MaxDurationReached marks a run the bound cut: a step before the run
	// folder failed after the bound fired, a unit was skipped for it, or a
	// server call of a unit was stopped by it. A bound that passed after the
	// last unit cut nothing and leaves this unset. Omitted when false, as
	// Cancelled is, so every older manifest stays byte-identical. Both can be
	// set; when they are, the bound came first.
	MaxDurationReached bool `json:"max_duration_reached,omitempty"`
```

Add to `collect/manifest.go` (import `strconv` and `time` if absent):

```go
// recordedBound reads the bound back from the run settings, where runConfig
// wrote it in whole seconds. False when the run had none.
func recordedBound(cfg map[string]string) (time.Duration, bool) {
	n, err := strconv.Atoi(cfg["max_duration_sec"])
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}
```

In `Human`, replace

```go
	if m.Run.DurationSec > 0 {
		fmt.Fprintf(&b, "Duration     : %d s\n", m.Run.DurationSec)
	}
```

with

```go
	// A cut run says so whatever its duration: a reader of this file alone has
	// no other way to learn that the run was stopped by its bound.
	switch d, ok := recordedBound(m.Config); {
	case m.Run.MaxDurationReached && ok:
		fmt.Fprintf(&b, "Duration     : %d s, stopped at the maximum duration of %s\n", m.Run.DurationSec, formatCeiling(d))
	case m.Run.MaxDurationReached:
		fmt.Fprintf(&b, "Duration     : %d s, stopped at its maximum duration\n", m.Run.DurationSec)
	case m.Run.DurationSec > 0:
		fmt.Fprintf(&b, "Duration     : %d s\n", m.Run.DurationSec)
	}
```

In `writeNotRun`, after the `dropped` map is filled:

```go
	// The bound's skips share one reason, built from the recorded bound, and
	// are written as one entry where the first of them falls.
	boundReason, bounded := "", 0
	if d, ok := recordedBound(m.Config); ok {
		boundReason = maxDurationSkipReason(d)
		for _, s := range m.Skipped {
			if s.Reason == boundReason {
				bounded++
			}
		}
	}
```

and in its loop, before `if s.Reason == skipDroppedDuringRun {`:

```go
		if boundReason != "" && s.Reason == boundReason {
			if bounded > 0 {
				noun := "collectors"
				if bounded == 1 {
					noun = "collector"
				}
				fmt.Fprintf(b, "  - %d %s not started, each listed in _run.json\n      %s\n", bounded, noun, s.Reason)
				bounded = 0
			}
			continue
		}
```

- [ ] Step 4: rerun: `top-level passes: 3`. Then `rtk proxy go test ./collect/ -count=1`.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| Put `m.Run.DurationSec > 0 &&` in the first case | `TestManifestTextSaysTheRunStoppedAtTheMaxDuration` (row 0) |
| Delete the grouping block in the loop | `TestManifestTextGroupsTheBoundsSkips` (198 occurrences) |
| Drop `omitempty` | `TestManifestWithoutABoundKeepsTodaysDurationLine` |

- [ ] Step 6: commit.

```bash
git add collect/manifest.go collect/manifest_test.go
git commit -m "Record in the manifest that the bound cut a run

run.max_duration_reached is the field every reader of the outcome will
consult, and it is omitted when false so older manifests stay identical.
MANIFEST.txt says it on the duration line, the only place a reader of that
file alone would learn it, and folds the bound's skips into one entry,
since a bound that fires early skips hundreds of units for one reason."
```

### Task 9: the note, the summary tail and the watch's notice

Spec: "How a bounded run is recorded" (the note's table, `summaryTail`), "After the run folder" (`watchOffNotice`). Criteria 13 (the note's table), 10 (`summaryTail`), 16b.

Files:
- Modify: `collect/maxduration.go` (`maxDurationNote`), `collect/collect.go` (`summaryTail`, the summary print in `Run`, the watch-off writes in `Run`), `collect/watch.go` (`watchOffNotice`)
- Test: `collect/maxduration_test.go`

Interfaces:
- Produces: `func maxDurationNote(limit time.Duration, notStarted int, stopped bool) string`; `func summaryTail(m *Manifest) string`; `func watchOffNotice(reason string, boundFired bool) (warning, note string)`.

- [ ] Step 1: tests in `collect/maxduration_test.go`:

```go
func TestMaxDurationNoteSaysWhatTheBoundCut(t *testing.T) {
	const pre = "note: the collection reached its maximum duration of 2h00m (7200 s); "
	for _, c := range []struct {
		notStarted int
		stopped    bool
		want       string
	}{
		{198, true, pre + "198 collectors were not started, and the one running then was stopped"},
		{198, false, pre + "198 collectors were not started"},
		{1, false, pre + "1 collector was not started"},
		{0, true, pre + "the collector running then was stopped"},
		{2, false, pre + "2 collectors were not started"},
	} {
		if got := maxDurationNote(2*time.Hour, c.notStarted, c.stopped); got != c.want {
			t.Errorf("(%d, %v) = %q, want %q", c.notStarted, c.stopped, got, c.want)
		}
	}
}

// The tokens of the line scripts parse, in the order things happened: the
// bound before the stop, since a stop first would have kept the bound from
// being recorded.
func TestSummaryTailOrdersTheBoundBeforeTheStop(t *testing.T) {
	for _, c := range []struct {
		name               string
		partial            int
		bound, cancelled   bool
		want               string
	}{
		{"nothing", 0, false, false, ""},
		{"partial units", 3, false, false, ", 3 partial"},
		{"the bound", 0, true, false, ", max duration reached"},
		{"a stop", 0, false, true, ", cancelled"},
		{"the bound then a stop", 0, true, true, ", max duration reached, cancelled"},
		{"partial units, the bound and a stop", 2, true, true, ", 2 partial, max duration reached, cancelled"},
	} {
		m := &Manifest{PartialUnits: c.partial}
		m.Run.MaxDurationReached, m.Run.Cancelled = c.bound, c.cancelled
		if got := summaryTail(m); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Once the bound has fired no collector will start, so a warning that
// nothing will cancel one describes a risk the run no longer runs. The
// question is the bound, asked when the warning would be written, and never
// the reason's text: a watch whose start failed after the bound keeps its own
// reason.
func TestWatchOffNoticeKeysOnTheBoundNotTheReason(t *testing.T) {
	reasons := []string{
		"not started: " + maxDurationText(2*time.Hour),
		"the collection session's id could not be read: context deadline exceeded",
		"its connection could not be opened: dial tcp 192.0.2.1:1433: i/o timeout",
	}
	for _, r := range reasons {
		if w, n := watchOffNotice(r, true); w != "" || n != "" {
			t.Errorf("bound fired, %q: got (%q, %q), want nothing", r, w, n)
		}
	}
	for _, r := range reasons[1:] {
		w, n := watchOffNotice(r, false)
		if want := "the blocking watch is off, " + r + ": nothing will cancel a collector that other sessions are waiting on"; w != want {
			t.Errorf("bound not fired, warning %q, want %q", w, want)
		}
		if want := "note: the blocking watch is off, " + r; n != want {
			t.Errorf("bound not fired, note %q, want %q", n, want)
		}
	}
}
```

- [ ] Step 2: run; does not compile.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^(TestMaxDurationNoteSaysWhatTheBoundCut|TestSummaryTailOrdersTheBoundBeforeTheStop|TestWatchOffNoticeKeysOnTheBoundNotTheReason)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined' "$LOG"
```

- [ ] Step 3: implement.

In `collect/maxduration.go`:

```go
// maxDurationNote is the one line the command line prints after the loop of
// a run the bound cut. notStarted counts the units skipped for the bound
// (skipBefore's byBound), so that it agrees with MANIFEST.txt's grouped
// entry; stopped says a unit was cut. Its second half depends on what the
// bound cut, since a bound that passed between units stopped nothing.
func maxDurationNote(limit time.Duration, notStarted int, stopped bool) string {
	n := "note: " + maxDurationText(limit) + "; "
	switch notStarted {
	case 0:
		return n + "the collector running then was stopped"
	case 1:
		n += "1 collector was not started"
	default:
		n += fmt.Sprintf("%d collectors were not started", notStarted)
	}
	if stopped {
		n += ", and the one running then was stopped"
	}
	return n
}
```

In `collect/watch.go`:

```go
// watchOffNotice is the warning and the stderr note of a watch that is not
// running. Once the bound has fired no collector will start, and both
// sentences describe a risk the run no longer runs, so both are empty. The
// caller asks boundReached at the moment it would write them, whatever the
// reason: a watch whose start began before the bound and failed after it
// keeps its own reason and still has no collector to protect.
func watchOffNotice(reason string, boundFired bool) (warning, note string) {
	if boundFired {
		return "", ""
	}
	return "the blocking watch is off, " + reason + ": nothing will cancel a collector that other sessions are waiting on",
		"note: the blocking watch is off, " + reason
}
```

In `collect/collect.go`, add next to `settleRun`:

```go
// summaryTail is the end of the line scripts parse after a run: the partial
// units, then the bound, then the stop, in the order they happened. A run the
// bound cut and the operator then stopped carries both, the bound first.
func summaryTail(m *Manifest) string {
	tail := ""
	if m.PartialUnits > 0 {
		tail = fmt.Sprintf(", %d partial", m.PartialUnits)
	}
	if m.Run.MaxDurationReached {
		tail += ", max duration reached"
	}
	if m.Run.Cancelled {
		tail += ", cancelled"
	}
	return tail
}
```

In `Run`, replace the block that builds `partial` (from `partial := ""` to the `fmt.Printf("%d result(s), ...` call) with:

```go
		fmt.Printf("%d result(s), %d skipped, %d error(s)%s\n%s\n",
			len(m.Results), len(m.Skipped), len(m.Errors), summaryTail(m), zipPath)
```

keeping the two comments that explain the partial count and the cancelled token above it, merged into one. In `Run`, replace

```go
	if watch == nil {
		m.warn("the blocking watch is off, " + reason +
			": nothing will cancel a collector that other sessions are waiting on")
		fmt.Fprintf(o.progress(), "note: the blocking watch is off, %s\n", reason)
	}
```

with

```go
	if watch == nil {
		if warning, note := watchOffNotice(reason, false); warning != "" {
			m.warn(warning)
			fmt.Fprintln(o.progress(), note)
		}
	}
```

The `false` is temporary: no bound exists yet, and Task 15 passes `boundReached(bound)` there.

- [ ] Step 4: rerun: `top-level passes: 3`. Then `rtk proxy go test ./... -count=1`.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| Append the stopped clause whatever `stopped` says | `TestMaxDurationNoteSaysWhatTheBoundCut` (row 2) |
| Swap the bound and cancelled tokens in `summaryTail` | `TestSummaryTailOrdersTheBoundBeforeTheStop` (the two rows with both) |
| Make `watchOffNotice` return empty when `strings.HasPrefix(reason, "not started: ")` instead of on `boundFired` | `TestWatchOffNoticeKeysOnTheBoundNotTheReason` (the failed-start row with the bound fired) |
| Propagation: in `Run`, print `fmt.Printf(... partial ...)` with the old local again | no test sees stdout of `Run` offline: predicted to stay green; the live tests do not read stdout either. Say so in the report. |

- [ ] Step 6: commit.

```bash
git add collect/maxduration.go collect/watch.go collect/collect.go collect/maxduration_test.go
git commit -m "Write the bound's note, the summary tokens and the watch notice once

Each of these sentences changes with what the bound cut, and each has a
case where the obvious wording is false: a bound between units stopped no
collector, a run cut and then stopped carries both tokens, and a watch
that is off after the bound protects nothing. One function each, so the
cases are tested without a server; the summary line and the watch notice
already go through them, with no bound yet."
```

### Task 10: the previous run of the day

Spec: "Exit code and the previous run of the day". Criteria 10 (`TestSettleRun`) and 11.

Files:
- Modify: `collect/collect.go` (`settleRun` parameter name and comment, the `skipLoses` comment table)
- Test: `collect/rerun_test.go`

Interfaces:
- Produces: `func settleRun(exit int, cutShort bool) (code int, discardPrevious bool)`, same rule.

- [ ] Step 1: tests. In `TestSettleRun`, rename the field `cancelled` to `cutShort` (and the message). Add to `collect/rerun_test.go`:

```go
// A unit skipped for the bound produced nothing, so a rerun that skipped it
// for the bound lost it, by the fallback's rule. Asserted by name, so that a
// case added above the fallback that happened to match the bound's reason
// fails here.
func TestSkipLosesCountsTheBoundsSkipAsALoss(t *testing.T) {
	for _, c := range []struct {
		reason string
		want   bool
	}{
		{maxDurationSkipReason(2 * time.Hour), true},
		{"the blocking watch cancelled 70.schema/055.page-density.sql on this database", true},
		{skipNotInQueryStoreInclude, false},
	} {
		if got := skipLoses(c.reason, runScope{}, runScope{}); got != c.want {
			t.Errorf("skipLoses(%q) = %v, want %v", c.reason, got, c.want)
		}
	}
}

// A bound that was not reached changed nothing collected, and one that was
// reached makes the run exit 2: settingsLost has nothing to say about it.
func TestSettingsLostIgnoresTheMaxDuration(t *testing.T) {
	for _, c := range [][2]map[string]string{
		{{"max_duration_sec": "7200"}, {}},
		{{}, {"max_duration_sec": "7200"}},
		{{"max_duration_sec": "7200"}, {"max_duration_sec": "3600"}},
	} {
		if lost := settingsLost(c[0], c[1]); len(lost) != 0 {
			t.Errorf("settingsLost(%v, %v) = %v, want nothing", c[0], c[1], lost)
		}
	}
}
```

- [ ] Step 2: run. `TestSkipLosesCountsTheBoundsSkipAsALoss` and `TestSettingsLostIgnoresTheMaxDuration` pass already (the fallback and the fixed list already give these answers): that is expected, these tests pin behaviour that must not change. `TestSettleRun` passes under its renamed field.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^(TestSettleRun|TestSkipLosesCountsTheBoundsSkipAsALoss|TestSettingsLostIgnoresTheMaxDuration)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected: `top-level passes: 3`.

- [ ] Step 3: implement. `settleRun(exit int, cutShort bool)`, body `if cutShort && exit == 0 { exit = 2 }`, and its comment gains: "cutShort is a run the operator stopped or the bound cut; either way the archive is partial and a 0 would tell a scheduler the collection succeeded." Its one caller still passes `m.Run.Cancelled` (Task 14 changes the call). In the `skipLoses` comment table, after the blocking watch row:

```
//	reached the maximum duration    yes, though such a run already exits 2 and
//	                                keeps prev as partial
```

- [ ] Step 4: rerun, 3 passes; full suite.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| In `skipLoses`, add above the fallback `if strings.HasSuffix(reason, " before this collector started") { return false }` | `TestSkipLosesCountsTheBoundsSkipAsALoss` |
| In `settingsLost`, add a comparison that names `max_duration_sec` when the two differ | `TestSettingsLostIgnoresTheMaxDuration` |
| In `settleRun`, drop the `cutShort` test | `TestSettleRun` |

- [ ] Step 6: commit.

```bash
git add collect/collect.go collect/rerun_test.go
git commit -m "Name settleRun's second parameter for a run cut short

The bound will pass through the same rule as the operator's stop: a run
cut short that would exit 0 exits 2, and only a run that exits 0 may delete
the one it replaced. The rule does not change; its parameter now says what
it covers. The rerun guard's table gains the bound's row, pinned by name."
```

### Task 11: the `Verdict` of `Observer.Finished`

Spec: "How a bounded run is recorded" (`Observer.Finished` carries the run's verdict, the checklist of implementations, `earned`, the count of collected units). Prepares criteria 6a, 13, 14.

Files:
- Modify: `collect/observer.go` (`Verdict`, `Observer.Finished`, the wrapper), `collect/collect.go` (`Run`: `earned`, `collected`, `finish`), `cmd/sql-auditor/progress.go`, `tui/observer.go` (`observer.Finished`, `finishedEvent`)
- Modify tests: `collect/observer_test.go` (`recordingObserver`), `collect/collect_test.go` (`TestRunLeavesAManifestWhenTheCorpusCannotBeRead`), `collect/dropped_live_test.go` (`dropOnObserver`), `cmd/sql-auditor/progress_test.go` (five `o.Finished(false)`), `tui/run_test.go` (`finishedEvent{cancelled: ...}`)
- Create: `collect/maxduration_live_test.go` (the live harness)

Interfaces:
- Produces:

```go
type Verdict struct {
	Cancelled          bool // run.cancelled
	MaxDurationReached bool // run.max_duration_reached
	// Failed is the run's own failure, before the stop or the bound is
	// applied: exit was 2 before settleRun, from a lint error or a unit
	// that failed and was not cut by the bound.
	Failed    bool
	Collected int // units whose runUnit returned no error, drops excluded
}
```

`Observer.Finished(v Verdict)`; `tui.finishedEvent{v collect.Verdict}`.

- [ ] Step 1: tests.

In `collect/observer_test.go`, `recordingObserver` gains `verdicts []Verdict`, and `Finished` becomes:

```go
func (r *recordingObserver) Finished(v Verdict) {
	word := "complete"
	if v.Cancelled {
		word = "cancelled"
	}
	r.finished = append(r.finished, word)
	r.verdicts = append(r.verdicts, v)
}
```

`TestObserverCallbacksAreSafeOnTheZeroValue` calls `o.Finished(Verdict{Cancelled: true})`. `TestObserverForwardsToTheWrappedImplementation` calls `o.Finished(Verdict{Cancelled: true, MaxDurationReached: true, Failed: true, Collected: 3})` and adds:

```go
	if len(rec.verdicts) != 1 || rec.verdicts[0] != (Verdict{Cancelled: true, MaxDurationReached: true, Failed: true, Collected: 3}) {
		t.Fatalf("Finished lost part of the verdict: %+v", rec.verdicts)
	}
```

In `TestRunLeavesAManifestWhenTheCorpusCannotBeRead`, add after the existing `rec.finished` assertion:

```go
	if len(rec.verdicts) != 1 || rec.verdicts[0] != (Verdict{}) {
		t.Errorf("verdict = %+v, want the zero verdict for a run that collected nothing and failed before the loop", rec.verdicts)
	}
```

Create `collect/maxduration_live_test.go` with the harness and its first test:

```go
package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// The bound against a real instance. Skipped unless SQL_AUDITOR_LIVE_SERVER
// is set (liveConfig):
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,11533 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run '^TestLiveMaxDuration' -v
//
// Nothing here creates a database or a table: every corpus is read-only
// instance collectors, and dbo.ZzMaxDurMissing is a name that is never
// created. The tests that set pauseHook reset it with a defer and never call
// t.Parallel, since the hook is a package variable.

// lockedBuf is a writer the run and the hook can share.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// maxDurRecorder keeps what Run hands its Observer that these tests assert
// on: the skips, the verdicts, and when the first unit came back.
type maxDurRecorder struct {
	mu        sync.Mutex
	skips     []*UnitSkipped
	verdicts  []Verdict
	firstDone time.Time
}

func (r *maxDurRecorder) Planned(int)                 {}
func (r *maxDurRecorder) UnitStarted(string, string) {}
func (r *maxDurRecorder) UnitDone(_, _ string, _ int64, _ time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.firstDone.IsZero() {
		r.firstDone = time.Now()
	}
	var sk *UnitSkipped
	if errors.As(err, &sk) {
		r.skips = append(r.skips, sk)
	}
}
func (r *maxDurRecorder) ScriptSkipped(string, string, string) {}
func (r *maxDurRecorder) Phase(string)                         {}
func (r *maxDurRecorder) Finished(v Verdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verdicts = append(r.verdicts, v)
}

func maxDurScript(timeout, sql string) []byte {
	return []byte("-- @scope:       instance\n-- @resultsets:  root:object\n-- @timeout:     " + timeout + "\n" +
		contractPreamble + sql + "\n")
}

const maxDurSelect = "SELECT @@VERSION AS [version] OPTION (RECOMPILE, MAXDOP 1);"

var (
	maxDurOne = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	maxDurFast = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("60", maxDurSelect)},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	// A first unit that fails on its own at once with 208, then a second.
	maxDurMissing = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("60", "SELECT * FROM dbo.ZzMaxDurMissing OPTION (RECOMPILE, MAXDOP 1);")},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	// A first unit that would wait a minute under a 30-minute @timeout.
	maxDurWait = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("1800", "WAITFOR DELAY '00:01:00';\n"+maxDurSelect)},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
	// A first unit whose own one-second @timeout expires first.
	maxDurOwnTimeout = fstest.MapFS{
		"queries/10.system/901.a.sql": {Data: maxDurScript("1", "WAITFOR DELAY '00:00:20';\n"+maxDurSelect)},
		"queries/10.system/902.b.sql": {Data: maxDurScript("60", maxDurSelect)},
	}
)

func maxDurConfig(t *testing.T, out string, bound time.Duration) Config {
	c := *liveConfig(t)
	// master, as TestLiveRunAddressReachesTheRerunGuard sets it: an
	// instance-scope unit run with an empty Database fails with 911 on the lab.
	c.Database, c.OutputDir = "master", out
	c.QueryTimeout = 30 * time.Second
	c.QueryStoreDays, c.QueryStoreTop = 7, 50
	c.MaxDuration = bound
	return c
}

type maxDurOut struct {
	code     int
	err      error
	m        Manifest
	human    string
	progress string
	debug    string
	rec      *maxDurRecorder
	began    time.Time
}

// maxDurRun runs one collection and reads back the manifest it wrote: the run
// folder's, or the failed-run folder's for a run that ended before it, never
// a run set aside as superseded.
func maxDurRun(t *testing.T, ctx context.Context, corpus fstest.MapFS, out string, now time.Time, bound time.Duration, dbg *lockedBuf) maxDurOut {
	t.Helper()
	c := maxDurConfig(t, out, bound)
	if dbg == nil {
		dbg = &lockedBuf{}
	}
	var prog lockedBuf
	rec := &maxDurRecorder{}
	began := time.Now()
	code, err := Run(ctx, Options{Config: &c, Corpus: corpus, Root: "queries", Now: now,
		Progress: &prog, Debug: dbg, Observer: rec})
	all, _ := filepath.Glob(filepath.Join(out, "*", manifestJSONName))
	var dirs []string
	for _, p := range all {
		if !strings.Contains(p, ".superseded-") {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("want one manifest of this run in %s, found %v", out, dirs)
	}
	b, rerr := os.ReadFile(filepath.Join(dirs[0], manifestJSONName))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var m Manifest
	if jerr := json.Unmarshal(b, &m); jerr != nil {
		t.Fatal(jerr)
	}
	human, _ := os.ReadFile(filepath.Join(dirs[0], manifestHumanName))
	o := maxDurOut{code, err, m, string(human), prog.String(), dbg.String(), rec, began}
	t.Logf("code=%d err=%v cancelled=%v reached=%v verdicts=%+v watch=%v/%q",
		code, err, m.Run.Cancelled, m.Run.MaxDurationReached, rec.verdicts, m.BlockingWatch.Enabled, m.BlockingWatch.Reason)
	for _, e := range m.Errors {
		t.Logf("  error: %s: %s (sql %d)", e.Script, e.Message, e.SQLError)
	}
	for _, s := range m.Skipped {
		t.Logf("  skipped: %s: %s", s.Script, s.Reason)
	}
	t.Logf("  warnings: %q", m.Warnings)
	t.Logf("  progress: %q", o.progress)
	return o
}

func (o maxDurOut) verdict(t *testing.T) Verdict {
	t.Helper()
	if len(o.rec.verdicts) != 1 {
		t.Fatalf("Finished was called %d times, want once", len(o.rec.verdicts))
	}
	return o.rec.verdicts[0]
}

// noteLines are the lines of Progress that the bound's note could be.
func noteLines(progress string) []string {
	var out []string
	for _, l := range strings.Split(progress, "\n") {
		if strings.HasPrefix(l, "note: the collection reached") {
			out = append(out, l)
		}
	}
	return out
}

// The verdict of a run with no bound: the count of collected units is Run's,
// a failed unit is not collected, and a failure of the run's own is Failed.
func TestLiveMaxDurationVerdictOfAnUnboundedRun(t *testing.T) {
	clean := maxDurRun(t, context.Background(), maxDurFast, t.TempDir(), time.Now(), 0, nil)
	if v := clean.verdict(t); clean.code != 0 || v != (Verdict{Collected: 2}) {
		t.Errorf("clean run: code %d, verdict %+v; want 0 and {Collected: 2}", clean.code, v)
	}
	failed := maxDurRun(t, context.Background(), maxDurMissing, t.TempDir(), time.Now(), 0, nil)
	if v := failed.verdict(t); failed.code != 2 || v != (Verdict{Failed: true, Collected: 1}) {
		t.Errorf("a failed unit: code %d, verdict %+v; want 2 and {Failed: true, Collected: 1}", failed.code, v)
	}
	if _, ok := clean.m.Config["max_duration_sec"]; ok {
		t.Error("an unbounded run recorded max_duration_sec")
	}
}
```

- [ ] Step 2: run the non-live set; the packages do not compile (`Verdict` undefined, `Finished` signature). Expected `go test exit: 1` for `collect`.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^(TestObserverCallbacksAreSafeOnTheZeroValue|TestObserverForwardsToTheWrappedImplementation|TestRunLeavesAManifestWhenTheCorpusCannotBeRead)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined' "$LOG"
```

- [ ] Step 3: implement.

`collect/observer.go`: add the `Verdict` type above, change the interface method to `Finished(v Verdict)` with its comment extended ("Finished reports the run's own verdict, taken from the manifest it has just written: whether the operator stopped it, whether the bound cut it, whether it had failed on its own before either, and how many units it collected. A caller cannot derive any of it: ..."), and the wrapper:

```go
func (w observer) Finished(v Verdict) {
	if w.o != nil {
		w.o.Finished(v)
	}
}
```

`collect/collect.go`, in `Run`: replace `exit := 0` with

```go
	exit := 0
	// earned is exit before settleRun: set wherever exit is set by a failure
	// of the run's own (a lint error, a unit that failed and was not cut by
	// the bound), and never by settleRun, whose result is 2 for every cut run.
	// Verdict.Failed is built from it and never from finish's code argument.
	// collected counts the units whose runUnit returned no error and whose
	// database was not found dropped: a unit the operator interrupted reaches
	// the observer as a UnitDone with no error, so no observer can count it.
	earned, collected := 0, 0
```

In `finish`, replace `obs.Finished(m.Run.Cancelled)` with:

```go
		obs.Finished(Verdict{
			Cancelled:          m.Run.Cancelled,
			MaxDurationReached: m.Run.MaxDurationReached,
			Failed:             earned != 0,
			Collected:          collected,
		})
```

(keep the comment above it, and add a sentence that `earned` and not `code` gives `Failed`). At `if len(planErrors) > 0 { exit = 2 }` add `earned = 2`. At `exit = code` in the loop add `earned = code` on the next line. After the line `m.NoteFailureDuration(s.Path, target.Name, took)` add:

```go
		if err == nil && report == nil {
			collected++
		}
```

(`err` is nil and `report` nil exactly when the unit succeeded and its database was not found dropped: `noteDropped` returns a non-nil report.)

`cmd/sql-auditor/progress.go`: `func (p *progress) Finished(collect.Verdict) { p.Done() }`, comment: it ignores the verdict, as it ignored the cancellation.

`tui/observer.go`: `func (o observer) Finished(v collect.Verdict) { o.send(finishedEvent{v: v}) }`, `type finishedEvent struct{ v collect.Verdict }`, `apply`: `s.Cancelled = e.v.Cancelled` (Task 20 adds the rest).

Test doubles: `dropOnObserver.Finished(Verdict) {}`; in `progress_test.go` the five `o.Finished(false)` become `o.Finished(collect.Verdict{})`; in `tui/run_test.go`, `finishedEvent{cancelled: true}` becomes `finishedEvent{v: collect.Verdict{Cancelled: true}}` and `{cancelled: false}` becomes `{}` (add the `collect` import if absent).

- [ ] Step 4: run the non-live command of step 2: `top-level passes: 3`. Then `rtk proxy go test ./... -count=1` (all three packages compile and pass). Then the live test:

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^TestLiveMaxDurationVerdictOfAnUnboundedRun$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected `top-level passes: 1`.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| The wrapper forwards `Verdict{Cancelled: v.Cancelled}` | `TestObserverForwardsToTheWrappedImplementation` |
| Count `collected++` on every unit (before the error handling) | `TestLiveMaxDurationVerdictOfAnUnboundedRun` (failed run: `Collected: 2`) |
| Delete `earned = code` | `TestLiveMaxDurationVerdictOfAnUnboundedRun` (`Failed` false) |
| `Failed: code != 0` in `finish` | predicted green here: without a bound, `code` equals `earned`. Criterion 6a (Task 16) catches it. Say so in the report. |

- [ ] Step 6: commit.

```bash
git add collect/observer.go collect/collect.go collect/observer_test.go collect/collect_test.go collect/dropped_live_test.go collect/maxduration_live_test.go cmd/sql-auditor/progress.go cmd/sql-auditor/progress_test.go tui/observer.go tui/run_test.go
git commit -m "Hand the observer the run's verdict as one value

The wizard will need to know, besides whether the run was stopped, whether
the bound cut it, whether it had failed on its own before that, and how
many units it collected; none of it can be derived from the events, since
an interrupted unit arrives as a UnitDone with no error. Run keeps the
failure it earned apart from settleRun's result, which is 2 for every cut
run and would make every bounded run look failed."
```

### Task 12: the bound's context and the steps before the run folder

Spec: "The bound's context", "The bound as a fact", "Before the first unit" (before the run folder, `pauseHook`). Criteria 5a and 5d.

Files:
- Modify: `collect/maxduration.go` (`pauseHook`, `pause`), `collect/collect.go` (`Run`: the bound's context, `stoppedOr`, `Connect`, the preflight, the probe, the listing), `collect/runner.go` (`Connect` reads its caller's context through `settled`)
- Test: `collect/maxduration_live_test.go`, `collect/maxduration_test.go`

Interfaces:
- Consumes: `settled`, `boundReached` (Task 6).
- Produces: `var pauseHook func(point string, bound context.Context)`; `func pause(point string, bound context.Context)`; in `Run`, the locals `limit time.Duration` and `bound context.Context`; the hook point `"leaving before the run folder"`, the first statement of `stoppedOr`.

- [ ] Step 1: live tests, appended to `collect/maxduration_live_test.go`:

```go
func boundSentence(limit time.Duration) string {
	return maxDurationText(limit) + " before the first collector: nothing was collected"
}

// Criterion 5a. A one-millisecond bound expires before Connect can complete.
// The run is the bound's, not an unreachable instance's: exit 2, the flag,
// the bound's sentence, and the step's words in a warning, not in errors.
func TestLiveMaxDurationBeforeTheFirstConnection(t *testing.T) {
	o := maxDurRun(t, context.Background(), maxDurFast, t.TempDir(), time.Now(), time.Millisecond, nil)
	if o.code != 2 {
		t.Errorf("exit %d, want 2", o.code)
	}
	if !o.m.Run.MaxDurationReached || o.m.Run.Cancelled {
		t.Errorf("reached %v, cancelled %v; want true, false", o.m.Run.MaxDurationReached, o.m.Run.Cancelled)
	}
	if o.err == nil || o.err.Error() != boundSentence(time.Millisecond) {
		t.Errorf("error %v, want %q", o.err, boundSentence(time.Millisecond))
	}
	warned := false
	for _, w := range o.m.Warnings {
		warned = warned || strings.HasPrefix(w, boundSentence(time.Millisecond)+"; the step in progress returned: cannot reach the instance: ")
	}
	if !warned {
		t.Errorf("no warning with the bound's sentence and the step's words: %q", o.m.Warnings)
	}
	if len(o.m.Errors) != 0 {
		t.Errorf("errors %+v, want none: the step's error belongs to the warning", o.m.Errors)
	}
	if v := o.verdict(t); v != (Verdict{MaxDurationReached: true}) {
		t.Errorf("verdict %+v, want {MaxDurationReached: true}", v)
	}
	if !strings.Contains(o.human, "Duration     : ") || !strings.Contains(o.human, "stopped at the maximum duration of") {
		t.Errorf("MANIFEST.txt does not say the run stopped at its bound:\n%s", o.human)
	}
}

// Criterion 5d. The bound, then a ctrl-c while the step it cut returns: both
// facts recorded, the bound's sentence returned, its warning written.
func TestLiveMaxDurationThenAStopInAFailingStep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	pauseHook = func(point string, b context.Context) {
		if point == "leaving before the run folder" && !called {
			called = true
			<-b.Done()
			cancel()
		}
	}
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, ctx, maxDurFast, t.TempDir(), time.Now(), time.Millisecond, nil)
	if !called {
		t.Fatal("the hook was never called: the run did not leave through stoppedOr")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached || !o.m.Run.Cancelled {
		t.Errorf("exit %d, reached %v, cancelled %v; want 2, true, true", o.code, o.m.Run.MaxDurationReached, o.m.Run.Cancelled)
	}
	if o.err == nil || o.err.Error() != boundSentence(time.Millisecond) {
		t.Errorf("error %v, want the bound's sentence", o.err)
	}
	warned := false
	for _, w := range o.m.Warnings {
		warned = warned || strings.HasPrefix(w, boundSentence(time.Millisecond)+"; the step in progress returned: ")
	}
	if !warned || len(o.m.Errors) != 0 {
		t.Errorf("warnings %q, errors %+v; want the bound's warning and no error", o.m.Warnings, o.m.Errors)
	}
	if v := o.verdict(t); v != (Verdict{Cancelled: true, MaxDurationReached: true}) {
		t.Errorf("verdict %+v, want {Cancelled: true, MaxDurationReached: true}", v)
	}
}
```

- [ ] Step 2: run; does not compile (`undefined: pauseHook`). Then, after adding only the `pauseHook` variable and `pause` (step 3, first part), run again and see both fail. With no bound context yet nothing cuts the run, so the one-millisecond run collects both units: `TestLiveMaxDurationBeforeTheFirstConnection` fails on exit 0 (want 2), the flag, the error (nil) and the missing warning, and `TestLiveMaxDurationThenAStopInAFailingStep` stops at `the hook was never called`, since nothing calls `pause` yet. Measured by the reviewer of 5 October: `code=0 err=<nil>`, two units collected.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveMaxDurationBeforeTheFirstConnection|TestLiveMaxDurationThenAStopInAFailingStep)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement.

In `collect/maxduration.go`:

```go
// pauseHook is a test seam, nil outside tests. Run calls it at five named
// points, so that a test can wait there on bound.Done(), which places the
// bound exactly at that point, and then cancel the run's context, which
// places a stop after it. Nothing outside tests sets it; each call costs a
// nil check.
var pauseHook func(point string, bound context.Context)

func pause(point string, bound context.Context) {
	if pauseHook != nil {
		pauseHook(point, bound)
	}
}
```

In `Run`, right after the `earned, collected := 0, 0` line of Task 11:

```go
	// The bound's context, and the only clock of this feature: every question
	// about the bound is whether a context's first cause is
	// errMaxDurationReached. ctx stays the run's context and is never
	// replaced by bound: a stop is read from ctx, and at the bound bound.Err()
	// is set, so every call that files a dead context as an operator's stop
	// would file the bound as one.
	limit := o.Config.MaxDuration
	bound, cancelBound := ctx, context.CancelFunc(func() {})
	if limit > 0 {
		bound, cancelBound = context.WithDeadlineCause(ctx, started.Add(limit), errMaxDurationReached)
	}
	defer cancelBound()
```

Replace `stoppedOr` (keep its comment, and add the paragraph below):

```go
	// With a bound, stoppedOr asks two facts and nothing of the error: whether
	// the bound fired first, and whether the operator stopped the run, both
	// every time, so that a run cut by the bound and then stopped records
	// both. When the bound fired, the step's error is quoted in the bound's
	// warning in its own words, since it may describe the cut or a failure of
	// the server's own in the same seconds, and the run does not try to tell.
	stoppedOr := func(code int, err error) (int, error) {
		pause("leaving before the run folder", bound)
		fired := boundReached(bound)
		stopped := stopRequested(ctx, m)
		if fired {
			m.Run.MaxDurationReached = true
			sentence := maxDurationText(limit) + " before the first collector: nothing was collected"
			warning := sentence
			if err != nil {
				warning += "; the step in progress returned: " + err.Error()
			}
			m.warn(warning)
			return finishWith("", 2, errors.New(sentence))
		}
		if stopped {
			return finishWith("", 2, errors.New("stopped before the first collector: nothing was collected"))
		}
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", code, err)
	}
```

Then pass `bound` instead of `ctx` to exactly these four calls: `Connect(ctx, db, o.Config)` (the first one, before the preflight), `runPreflightWithDeadline(ctx, conn, o.Config)`, `probeWithDeadline(ctx, conn, o.Config)`, `candidatesWithDeadline(ctx, conn, o.Config)`. Nothing else changes in this task.

- [ ] Step 4: run step 2's command: `top-level passes: 2`. Then the non-live suite, and `TestLiveMaxDurationVerdictOfAnUnboundedRun` once more (an unbounded run must be unchanged).

- [ ] Step 5: breaks.

| Change | Test that must fail | Why |
| --- | --- | --- |
| Delete the `if fired {...}` branch | `TestLiveMaxDurationBeforeTheFirstConnection` (exit 1) | `stoppedOr` must ask the bound |
| Ask the stop first and return on it (move `if stopped` above `if fired`) | `TestLiveMaxDurationThenAStopInAFailingStep` (flag, sentence, warning, verdict) | version 4's order |
| Append the step's error to `m.Errors` before the branches | `TestLiveMaxDurationBeforeTheFirstConnection` (errors not empty) | the warning, not errors |
| Delete `m.Run.MaxDurationReached = true` in the branch | both (flag and verdict) | propagation downstream: the fact must reach the record |
| `Connect(ctx, ...)` again | `TestLiveMaxDurationBeforeTheFirstConnection` (the warning quotes the preflight, not `cannot reach the instance`) | propagation upstream: the step must run under the bound |

The bound passed before `Connect` in 5a makes `TestLiveMaxDurationBeforeTheFirstConnection` stay green when `stoppedOr` asks the stop first (it has no stop): predicted green, which is why 5d exists.

- [ ] Step 6: `Connect`'s own reading of its caller's context. `Connect` rewords a deadline error as a login timeout when its budget ran out and its caller's context did not (`ctx.Err() == nil`). Now that its caller's context is the bound, that test meets the instant `settled` exists for: the dial fails with `i/o timeout` while `ctx.Err()` is still nil, and the run's warning would quote a login timeout of twice `SQL_CONNECT_TIMEOUT_SEC` about a dial the bound cut. `boundReached` already keeps the flag and the exit code right; this is the words. Test, appended to `collect/maxduration_test.go` (add `net` to its imports):

```go
// Connect calls a deadline error a login timeout only when its own budget ran
// out. A dial cut by the caller's deadline, in the instant before the
// caller's timer has cancelled its context, keeps the dial's own words.
// Deterministic: the caller's context is cancelled only after Connect has had
// a hundred milliseconds to answer wrongly, and an unfixed Connect answers in
// microseconds, since a dial whose deadline has passed is refused before any
// packet is sent.
func TestConnectLeavesADialItsCallersDeadlineCutInItsOwnWords(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer ln.Close()
	cfg := &Config{Server: ln.Addr().String(), User: "AUDIT_RO", Password: "x",
		AppName: "sql-auditor-test", ConnectTimeout: time.Minute}
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	inner, fire := context.WithCancelCause(context.Background())
	defer fire(nil)
	ctx := pastDeadline{inner, time.Now().Add(-time.Millisecond)}
	go func() {
		time.Sleep(100 * time.Millisecond)
		fire(errMaxDurationReached)
	}()
	c, err := Connect(ctx, db, cfg)
	if c != nil {
		c.Close()
		t.Fatal("got a connection")
	}
	if err == nil || strings.Contains(err.Error(), "did not complete the login") {
		t.Errorf("err = %v, want the dial's own words, not a login timeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want it to wrap the deadline", err)
	}
}
```

Run it; it fails, `err = the server took the connection but did not complete the login within 2m0s (twice SQL_CONNECT_TIMEOUT_SEC)`:

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^TestConnectLeavesADialItsCallersDeadlineCutInItsOwnWords$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |did not complete' "$LOG"
```

Then in `Connect` (`collect/runner.go`), the test `ctx.Err() == nil` becomes `settled(ctx).Err() == nil`, with this comment above the `if`:

```go
	// settled: a dial bounded by ctx's own deadline (the bound's, when Run
	// connects) fails before ctx's timer has cancelled it, and ctx.Err() read
	// in that instant would call the caller's deadline a login timeout of
	// ours. When cctx's budget ran out first, ctx's deadline has not passed
	// and settled returns at once.
```

Rerun: `top-level passes: 1`. Break: `ctx.Err() == nil` again: red on every run, with the message above. `TestLiveMaxDurationBeforeTheFirstConnection` cannot see this break (the reworded message still begins `cannot reach the instance: `), which is why the test is offline and of its own. Measured on 5 October 2026 on a copy of the tree: 50 runs of 50 green with the change, 50 of 50 red without it, under `GOMAXPROCS=1`.

- [ ] Step 7: commit.

```bash
git add collect/maxduration.go collect/collect.go collect/runner.go collect/maxduration_test.go collect/maxduration_live_test.go
git commit -m "Cut the steps before the run folder at the bound

The connection, the preflight, the probe and the listing now run under the
bound's context, and stoppedOr records whether the bound fired first as a
fact read from that context, beside the operator's stop. Without it a
Connect cut by the bound came back as exit 1 about an instance that was
answering. The step's own words go into the bound's warning rather than
into errors, since the run cannot tell a cut from a coincident failure.
Connect no longer calls a dial the bound cut a login timeout: in the
instant before the bound's timer runs, its context does not yet say it
has expired, so it is read once its Done has closed."
```

### Task 13: the check before `lockRun`

Spec: "Before the first unit" (the check before `lockRun`, the `Debug` line `N database(s) listed`). Criteria 5b and 5c.

Files:
- Modify: `collect/collect.go` (`Run`, after the listing and before `lockRun`)
- Test: `collect/maxduration_live_test.go`

Interfaces:
- Produces: the `Debug` line `N database(s) listed`, written right after `candidatesWithDeadline` returns without error; the hook point `"before the run folder"`.

- [ ] Step 1: tests, appended:

```go
// plantPreviousRun puts a folder and an archive at the name this run will
// use, from RunFolderFor with the server name read on the test's own
// connection, as a same-day earlier run would have left them.
func plantPreviousRun(t *testing.T, out string, now time.Time) string {
	t.Helper()
	cfg := maxDurConfig(t, out, 0)
	db, err := Open(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := Connect(context.Background(), db, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	si, err := Probe(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	folder := RunFolderFor(out, RunServerName(si.Name, &cfg), "", now, false)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(folder+".zip", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return folder
}

func atTheCheckBeforeTheRunFolder(t *testing.T, stopAfter bool) (maxDurOut, bool, bool, string) {
	out, now := t.TempDir(), time.Now()
	folder := plantPreviousRun(t, out, now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dbg := &lockedBuf{}
	called, listed := false, false
	pauseHook = func(point string, b context.Context) {
		if point == "before the run folder" && !called {
			called = true
			listed = strings.Contains(dbg.String(), "database(s) listed")
			<-b.Done()
			if stopAfter {
				cancel()
			}
		}
	}
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, ctx, maxDurFast, out, now, 3*time.Second, dbg)
	return o, called, listed, folder
}

// Criterion 5b. A bound reached after the listing and before lockRun does
// not go on to set the previous run of the day aside.
func TestLiveMaxDurationAtTheCheckBeforeTheRunFolder(t *testing.T) {
	o, called, listed, folder := atTheCheckBeforeTheRunFolder(t, false)
	if !called {
		t.Fatal("the hook was never called: either the preamble outlasted three seconds or the check is missing")
	}
	if !listed {
		t.Error("the hook ran before the listing returned: no 'database(s) listed' line in Debug yet")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached {
		t.Errorf("exit %d, reached %v; want 2, true", o.code, o.m.Run.MaxDurationReached)
	}
	if _, err := os.Stat(filepath.Join(folder, "marker")); err != nil {
		t.Errorf("the planted run folder was touched: %v", err)
	}
	if _, err := os.Stat(folder + ".zip"); err != nil {
		t.Errorf("the planted archive was touched: %v", err)
	}
	if aside, _ := filepath.Glob(filepath.Join(filepath.Dir(folder), "*.superseded-*")); len(aside) != 0 {
		t.Errorf("something was set aside: %v", aside)
	}
}

// Criterion 5c. The bound, then a stop, at the same check: both recorded,
// the bound's sentence returned.
func TestLiveMaxDurationThenAStopAtTheCheckBeforeTheRunFolder(t *testing.T) {
	o, called, _, _ := atTheCheckBeforeTheRunFolder(t, true)
	if !called {
		t.Fatal("the hook was never called")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached || !o.m.Run.Cancelled {
		t.Errorf("exit %d, reached %v, cancelled %v; want 2, true, true", o.code, o.m.Run.MaxDurationReached, o.m.Run.Cancelled)
	}
	if o.err == nil || o.err.Error() != boundSentence(3*time.Second) {
		t.Errorf("error %v, want %q", o.err, boundSentence(3*time.Second))
	}
	if v := o.verdict(t); v != (Verdict{Cancelled: true, MaxDurationReached: true}) {
		t.Errorf("verdict %+v", v)
	}
}
```

- [ ] Step 2: run; both fail (`the hook was never called`).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveMaxDurationAtTheCheckBeforeTheRunFolder|TestLiveMaxDurationThenAStopAtTheCheckBeforeTheRunFolder|TestLiveMaxDurationBeforeTheFirstConnection|TestLiveMaxDurationThenAStopInAFailingStep)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected before: `top-level passes: 2` (Task 12's two).

- [ ] Step 3: implement. Right after

```go
	cands, err := candidatesWithDeadline(bound, conn, o.Config)
	if err != nil {
		return stoppedOr(1, err)
	}
```

add

```go
	// After the call returned, not before it: "listing the databases" above is
	// written before the call, and a test that places the bound relative to
	// the listing needs a line the listing itself guards.
	o.Debugf("%d database(s) listed", len(cands))
```

Immediately before `releaseLock, err := lockRun(runFolder, o.Now)` (after `runFolder := RunFolderFor(...)`):

```go
	// The bound is asked once more here, before lockRun and prepareRunFolder,
	// which is where the previous run of the day is set aside: a bound reached
	// during the last server step, or the local steps since, must not go on to
	// move it. Through stoppedOr, so that the order of the two facts is
	// written once; stoppedOr finds the bound fired, since a context's cause
	// never changes once set, and has no step error to quote.
	pause("before the run folder", bound)
	if boundReached(bound) {
		return stoppedOr(2, nil)
	}
```

- [ ] Step 4: run step 2's command: `top-level passes: 4`.

- [ ] Step 5: breaks.

| Change | Test that must fail | Prediction |
| --- | --- | --- |
| Delete the check and its hook call | `TestLiveMaxDurationAtTheCheckBeforeTheRunFolder` (hook never called; the run would set the planted run aside) | red |
| Move the hook and the check just before `candidatesWithDeadline` (after `listing the databases`) | `TestLiveMaxDurationAtTheCheckBeforeTheRunFolder` (`database(s) listed` not yet written) | red |
| In `stoppedOr`, ask the stop first and return on it | `TestLiveMaxDurationThenAStopAtTheCheckBeforeTheRunFolder` and `TestLiveMaxDurationThenAStopInAFailingStep` | red, red |
| Give the check a branch of its own (`if boundReached(bound) { stopRequested(ctx, m); m.Run.MaxDurationReached = true; ...; return finishWith(...) }`) and ask the stop first in `stoppedOr` | `TestLiveMaxDurationThenAStopInAFailingStep` red, `TestLiveMaxDurationThenAStopAtTheCheckBeforeTheRunFolder` green | the reason 5d exists; report both |

- [ ] Step 6: commit.

```bash
git add collect/collect.go collect/maxduration_live_test.go
git commit -m "Ask the bound before the run folder sets the previous run aside

prepareRunFolder is where the previous run of the same server and day is
renamed aside, so a bound reached during the listing or the local steps
after it must end the run before that point, with nothing touched. The
check goes through stoppedOr so that the order of the bound and the stop
is written in one place. A Debug line after the listing lets a test place
the bound relative to it."
```

### Task 14: the loop after the bound

Spec: "Before each unit", "After a unit", "How a bounded run is recorded" (the note, `UnitSkipped.MaxDuration`), "Exit code and the previous run of the day" (`settleRun` given the bound). Criteria 6c, 10 (cut between units), 9 (absent when the bound passes after the last unit).

Files:
- Modify: `collect/watch.go` (`UnitSkipped`), `collect/collect.go` (the loop, the note after it, the `settleRun` call)
- Test: `collect/maxduration_live_test.go`

Interfaces:
- Consumes: `skipBefore`, `maxDurationNote`, `settleRun(exit, cutShort)`.
- Produces: `UnitSkipped{Reason string; MaxDuration bool}`; the hook point `"after a unit"`; loop locals `notStarted int`, `stoppedByBound bool` (set in Task 16).

- [ ] Step 1: tests, appended:

```go
// waitOnceAt returns a hook that waits for the bound at its first call at
// point, and does nothing at any other.
func waitOnceAt(point string, called *bool) func(string, context.Context) {
	return func(p string, b context.Context) {
		if p == point && !*called {
			*called = true
			<-b.Done()
		}
	}
}

// Criterion 10, a run cut only between two units: no unit fails, so exit
// would stay 0 unless settleRun is given the bound.
func TestLiveMaxDurationBetweenTwoUnits(t *testing.T) {
	called := false
	pauseHook = waitOnceAt("after a unit", &called)
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, context.Background(), maxDurFast, t.TempDir(), time.Now(), 3*time.Second, nil)
	if !called {
		t.Fatal("the hook was never called")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached {
		t.Errorf("exit %d, reached %v; want 2, true", o.code, o.m.Run.MaxDurationReached)
	}
	if len(o.m.Results) != 1 || len(o.m.Skipped) != 1 || o.m.Skipped[0].Reason != maxDurationSkipReason(3*time.Second) {
		t.Errorf("results %d, skipped %+v; want one result and one skip for the bound", len(o.m.Results), o.m.Skipped)
	}
	if len(o.rec.skips) != 1 || !o.rec.skips[0].MaxDuration {
		t.Errorf("the observer was not told the skip was the bound's: %+v", o.rec.skips)
	}
	if nl := noteLines(o.progress); len(nl) != 1 || nl[0] != "note: the collection reached its maximum duration of 0m03s (3 s); 1 collector was not started" {
		t.Errorf("note lines %q", nl)
	}
	if strings.Contains(o.progress, "connection lost") {
		t.Error("connection lost printed")
	}
	if v := o.verdict(t); v != (Verdict{MaxDurationReached: true, Collected: 1}) {
		t.Errorf("verdict %+v, want {MaxDurationReached: true, Collected: 1}", v)
	}
	if o.m.Config["max_duration_sec"] != "3" {
		t.Errorf("max_duration_sec %q, want 3", o.m.Config["max_duration_sec"])
	}
}

// Criterion 9: a bound that passes after the last unit cut nothing.
func TestLiveMaxDurationAfterTheLastUnitCutsNothing(t *testing.T) {
	called := false
	pauseHook = waitOnceAt("after a unit", &called)
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, context.Background(), maxDurOne, t.TempDir(), time.Now(), 3*time.Second, nil)
	if !called {
		t.Fatal("the hook was never called")
	}
	if o.code != 0 || o.m.Run.MaxDurationReached || len(noteLines(o.progress)) != 0 {
		t.Errorf("exit %d, reached %v, notes %q; want 0, false, none", o.code, o.m.Run.MaxDurationReached, noteLines(o.progress))
	}
	if v := o.verdict(t); v != (Verdict{Collected: 1}) {
		t.Errorf("verdict %+v, want {Collected: 1}", v)
	}
}

// Criterion 6c. A failure earned before the bound: the unit's 208 keeps its
// words, the bound skips the rest without pinging a connection it no longer
// needs, and the run's own failure is in the verdict.
func TestLiveMaxDurationAfterAFailureOfTheUnitsOwn(t *testing.T) {
	called := false
	pauseHook = waitOnceAt("after a unit", &called)
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, context.Background(), maxDurMissing, t.TempDir(), time.Now(), 3*time.Second, nil)
	if !called {
		t.Fatal("the hook was never called")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached {
		t.Errorf("exit %d, reached %v; want 2, true", o.code, o.m.Run.MaxDurationReached)
	}
	if len(o.m.Errors) != 1 || o.m.Errors[0].SQLError != 208 || strings.Contains(o.m.Errors[0].Message, "stopped when") {
		t.Errorf("errors %+v; want the unit's 208 in its own words", o.m.Errors)
	}
	if len(o.m.Skipped) != 1 || o.m.Skipped[0].Reason != maxDurationSkipReason(3*time.Second) {
		t.Errorf("skipped %+v; want the second unit skipped for the bound", o.m.Skipped)
	}
	if strings.Contains(o.progress, "connection lost") {
		t.Error("connection lost printed about a connection that was fine")
	}
	if v := o.verdict(t); !v.MaxDurationReached || !v.Failed {
		t.Errorf("verdict %+v, want MaxDurationReached and Failed", v)
	}
}
```

- [ ] Step 2: run; all three fail (the hook point does not exist yet: `the hook was never called`).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveMaxDurationBetweenTwoUnits|TestLiveMaxDurationAfterTheLastUnitCutsNothing|TestLiveMaxDurationAfterAFailureOfTheUnitsOwn)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement.

`collect/watch.go`:

```go
// UnitSkipped is what Observer.UnitDone carries for a planned unit the run
// decided, once running, not to execute. It is not a failure: the gauge counts
// the unit, since Planned did, and the screens show it as a skip.
//
// MaxDuration marks the bound's skips. A bound that fires early skips
// hundreds of units for one reason, so the screens count them without a line
// each; the note after the loop and the wizard's last screen say it once.
type UnitSkipped struct {
	Reason      string
	MaxDuration bool
}
```

In `Run`, beside `watchNoted := false`: `notStarted, stoppedByBound := 0, false`. Replace the start of the loop body (from `reason, ok := heldBack(...)` to the `continue` of the skip) with:

```go
		reason, byBound, skip := skipBefore(cancelledOn, droppedOn, bound, limit, target.Name)
		if skip {
			if byBound {
				m.Run.MaxDurationReached = true
				notStarted++
			}
			m.Skipped = append(m.Skipped, SkippedScript{Script: s.Path, Target: target.Name, Reason: reason})
			// UnitDone and not ScriptSkipped: this unit was planned, and one
			// UnitDone per planned unit is what brings the gauge to its total.
			obs.UnitDone(s.Path, target.Name, 0, 0, &UnitSkipped{Reason: reason, MaxDuration: byBound})
			continue
		}
```

Right after `err := runUnit(ctx, conn, o, m, rw, s, target, watch, spid)`: `pause("after a unit", bound)`.

In the catalog check closure: `databaseExists(bound, conn, o.Config, name)`, with a comment: under the bound it fails at once once the bound has passed, and a check that could not be made reads as "not shown dropped"; a real drop in the same instant as the bound is filed as an error, in a run that exits 2 for the bound anyway.

Replace the success branch with:

```go
		if err == nil {
			// After a unit: the stop, then the bound. Once the bound has
			// passed the loop neither recycles nor pings nor reconnects; the
			// units left are skipped and nothing after the loop uses the
			// connection.
			if stopRequested(ctx, m) {
				break
			}
			if boundReached(bound) {
				continue
			}
			if conn, spid, err = recycleConn(bound, db, conn, o.Config); err != nil {
				if stopRequested(ctx, m) {
					break
				}
				if boundReached(bound) {
					continue
				}
				err = fmt.Errorf("session reset after %s failed: %w", s.Path, err)
				m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
				return finishWith(runFolder, 1, err)
			}
			continue
		}
```

After `if stopRequested(ctx, m) { break }` (the one before `connAlive`), and in the reconnect:

```go
		if boundReached(bound) {
			continue
		}
		if !connAlive(bound, conn, o.Config) {
			// A ping on a context whose bound has fired fails at once, so a
			// bound landing during the ping would print a lost connection
			// about one that was fine. The stop, then the bound, before a word.
			if stopRequested(ctx, m) {
				break
			}
			if boundReached(bound) {
				continue
			}
			fmt.Fprintln(o.progress(), "connection lost; attempting one reconnect")
			conn.Close()
			fresh, cerr := Connect(bound, db, o.Config)
			if cerr != nil {
				if stopRequested(ctx, m) {
					break
				}
				if boundReached(bound) {
					continue
				}
				cerr = fmt.Errorf("reconnect failed: %w", cerr)
				m.Errors = append(m.Errors, ErrorEntry{Message: cerr.Error()})
				return finishWith(runFolder, 1, cerr)
			}
			conn = fresh
			if rerr := resetWithDeadline(bound, conn, o.Config); rerr != nil {
				if stopRequested(ctx, m) {
					break
				}
				if boundReached(bound) {
					continue
				}
				rerr = fmt.Errorf("session reset after reconnect failed: %w", rerr)
				m.Errors = append(m.Errors, ErrorEntry{Message: rerr.Error()})
				return finishWith(runFolder, 1, rerr)
			}
			// A new connection is a new session: watching the old id would
			// watch nothing. Zero, if it cannot be read, matches no waiter.
			// Under the bound: its error is ignored, and on the run's context
			// it would be a query after the bound with nothing to report it.
			spid, _ = sessionID(bound, conn, o.Config)
		} else if conn, spid, err = recycleConn(bound, db, conn, o.Config); err != nil {
			if stopRequested(ctx, m) {
				break
			}
			if boundReached(bound) {
				continue
			}
			err = fmt.Errorf("session reset after %s failed: %w", s.Path, err)
			m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
			return finishWith(runFolder, 1, err)
		}
```

(The two existing comments of this region, on asking the stop again and on the reconnect, stay.) After the loop, before `obs.Phase("writing manifest")`:

```go
	// One line for what the bound cut, counted from the bound's own skips so
	// that it agrees with MANIFEST.txt; a skip per line would be hundreds.
	if m.Run.MaxDurationReached {
		fmt.Fprintln(o.progress(), maxDurationNote(limit, notStarted, stoppedByBound))
	}
```

and `settleRun(exit, m.Run.Cancelled)` becomes `settleRun(exit, m.Run.Cancelled || m.Run.MaxDurationReached)`.

`stoppedByBound` is declared and read but never set until Task 16: Go accepts it (it is used).

- [ ] Step 4: run step 2's command: `top-level passes: 3`. Then all live max-duration tests so far, as a regression set (count 8): `-run '^TestLiveMaxDuration'` must report `top-level passes: 8`, and `TestLiveRunAddressReachesTheRerunGuard` (read-only, touches the loop): `-run '^TestLiveRunAddressReachesTheRerunGuard$'`, 1 pass. Do not run the live tests that create `ZzDroppedDuringRun` or `ZzWatchLive`.

- [ ] Step 5: breaks.

| Change | Test that must fail | Prediction |
| --- | --- | --- |
| `settleRun(exit, m.Run.Cancelled)` again | `TestLiveMaxDurationBetweenTwoUnits` (exit 0) | red |
| `&UnitSkipped{Reason: reason}` (no `MaxDuration`) | `TestLiveMaxDurationBetweenTwoUnits` | red |
| Delete `m.Run.MaxDurationReached = true` on `byBound` | `TestLiveMaxDurationBetweenTwoUnits` (flag, exit 0) | red |
| Set the flag after the loop when `boundReached(bound)` | `TestLiveMaxDurationAfterTheLastUnitCutsNothing` | red |
| Remove the guard before `connAlive` only | `TestLiveMaxDurationAfterAFailureOfTheUnitsOwn` | green: the guard after the failed ping does the same work (measured on the prototype). Report it. |
| Remove the guard after a failed `connAlive` only | same | green, for the converse reason |
| Remove both | same (`connection lost` printed; the reconnect's own guard still keeps the run from exit 1) | red |
| `databaseExists(ctx, ...)` again | none here (no 911): predicted green. The review checks it by reading. | green |

- [ ] Step 6: commit.

```bash
git add collect/watch.go collect/collect.go collect/maxduration_live_test.go
git commit -m "Skip the units left once the bound has passed

Every unit that reaches the loop after the bound is skipped with the
bound's reason, after the watch's and the drop's, and the loop stops
pinging, recycling and reconnecting, so a bound passing just after a
failure costs neither a lost-connection message nor an exit 1. A run cut
only between units failed nothing and would have exited 0, so settleRun
now takes the bound beside the operator's stop. One note after the loop
says what was not started."
```

### Task 15: the check before the blocking watch

Spec: "Before the first unit" (after the run folder: the session id under the bound, `the collection session is N`, the check before the watch, the watch's reason, `watchOffNotice`). Criterion 16a.

Files:
- Modify: `collect/collect.go` (`Run`, the session id and the watch's start)
- Test: `collect/maxduration_live_test.go`

Interfaces:
- Produces: the `Debug` line `the collection session is N`; the hook point `"before the blocking watch"`; `blocking_watch.reason` `not started: ` followed by `maxDurationText(limit)`.

- [ ] Step 1: test, appended:

```go
// Criterion 16a. A same-day rerun without --keep, cut after the run folder
// and before the watch: both units skipped, the watch not started and not
// warned about, the run partial, the first run kept.
func TestLiveMaxDurationAfterTheRunFolderKeepsThePreviousRun(t *testing.T) {
	out, now := t.TempDir(), time.Now()
	if first := maxDurRun(t, context.Background(), maxDurFast, out, now, 0, nil); first.code != 0 {
		t.Fatalf("first run exit %d", first.code)
	}
	dbg := &lockedBuf{}
	called, sessionRead := false, false
	pauseHook = func(point string, b context.Context) {
		if point == "before the blocking watch" && !called {
			called = true
			sessionRead = strings.Contains(dbg.String(), "the collection session is ")
			<-b.Done()
		}
	}
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, context.Background(), maxDurFast, out, now, 3*time.Second, dbg)
	if !called {
		t.Fatal("the hook was never called")
	}
	if !sessionRead {
		t.Error("the hook ran before the session id was read")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached || len(o.m.Results) != 0 {
		t.Errorf("exit %d, reached %v, results %d; want 2, true, 0", o.code, o.m.Run.MaxDurationReached, len(o.m.Results))
	}
	reason := maxDurationSkipReason(3 * time.Second)
	if len(o.m.Skipped) != 2 || o.m.Skipped[0].Reason != reason || o.m.Skipped[1].Reason != reason {
		t.Errorf("skipped %+v; want both units for the bound", o.m.Skipped)
	}
	if len(o.rec.skips) != 2 || !o.rec.skips[0].MaxDuration || !o.rec.skips[1].MaxDuration {
		t.Errorf("skips told to the observer: %+v", o.rec.skips)
	}
	if o.m.BlockingWatch.Enabled || o.m.BlockingWatch.Reason != "not started: the collection reached its maximum duration of 0m03s (3 s)" {
		t.Errorf("watch %v, reason %q", o.m.BlockingWatch.Enabled, o.m.BlockingWatch.Reason)
	}
	for _, w := range o.m.Warnings {
		if strings.Contains(w, "nothing will cancel") {
			t.Errorf("warning about a watch that has nothing to protect: %q", w)
		}
	}
	if strings.Contains(o.progress, "note: the blocking watch is off") {
		t.Error("stderr note about the watch")
	}
	if nl := noteLines(o.progress); len(nl) != 1 || !strings.HasSuffix(nl[0], "; 2 collectors were not started") {
		t.Errorf("note lines %q", nl)
	}
	if !strings.Contains(o.progress, "this run is partial, so the run it replaced was kept:") {
		t.Error("Progress does not say the run it replaced was kept")
	}
	zips, _ := filepath.Glob(filepath.Join(out, "*.zip"))
	aside, _ := filepath.Glob(filepath.Join(out, "*.superseded-*"))
	if len(zips) == 0 || len(aside) == 0 {
		t.Errorf("archives %v, set aside %v; want this run's archive and the first run kept", zips, aside)
	}
}
```

- [ ] Step 2: run; fails (hook never called).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^TestLiveMaxDurationAfterTheRunFolderKeepsThePreviousRun$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement. Replace the block from `spid, spidErr := sessionID(ctx, conn, o.Config)` to the end of the `if watch == nil {...}` block with:

```go
	spid, spidErr := sessionID(bound, conn, o.Config)
	if spidErr == nil {
		// After a successful read, so that a test can place the bound after it.
		o.Debugf("the collection session is %d", spid)
	}
	// The bound is asked before the watch starts: once it has passed no
	// collector will run, and a watch would protect nothing. The reason says
	// only that the bound had passed, which is true of every plan; whether it
	// cut anything is max_duration_reached's to say. The watch's start itself
	// stays on the run's context, and is not cut by the bound.
	pause("before the blocking watch", bound)
	var stopWatch func()
	reason := ""
	switch {
	case boundReached(bound):
		reason = "not started: " + maxDurationText(limit)
	case spidErr != nil:
		reason = "the collection session's id could not be read: " + spidErr.Error()
	default:
		watch, stopWatch, reason = startBlockingWatch(ctx, o.Config, denied, spid)
		defer stopWatch()
	}
	m.BlockingWatch.Enabled, m.BlockingWatch.Reason = watch != nil, reason
	if watch == nil {
		// Asked at the moment of writing, whatever the reason: a start that
		// began before the bound and failed after it keeps its own reason
		// and has no collector to protect either.
		if warning, note := watchOffNotice(reason, boundReached(bound)); warning != "" {
			m.warn(warning)
			fmt.Fprintln(o.progress(), note)
		}
	}
```

(keep the existing comment "The blocking watch starts last, ..." above it).

- [ ] Step 4: run: `top-level passes: 1`. Then `-run '^TestLiveMaxDuration'`: `top-level passes: 9`.

- [ ] Step 5: breaks.

| Change | Prediction |
| --- | --- |
| Delete the `case boundReached(bound):` | red: the session id was read before the bound, so the watch starts and `enabled` is true |
| Move the hook and the check before `sessionID` | red: `the collection session is` not yet written |
| `watchOffNotice(reason, false)` | red: the watch's note and warning appear |
| `settleRun(exit, m.Run.Cancelled)` (Task 14's break, again) | red: exit 0; the first run is still kept, by `previousRunLost` |
| Delete the `Debugf` line | red: `sessionRead` false. A test that rests on a line written for it: the review checks the line's placement by reading. |

- [ ] Step 6: commit.

```bash
git add collect/collect.go collect/maxduration_live_test.go
git commit -m "Do not start the blocking watch once the bound has passed

After the run folder, a bound that passes before the first unit leaves no
collector to protect: the watch is not started, its reason says the bound
had passed, and no warning claims that nothing will cancel a collector.
The run goes on into the loop, which skips every unit, and keeps the run it
replaced as any partial rerun does."
```

### Task 16: units under the bound: `cut`

Spec: "During a unit" (the unit's context from `bound`, the first reset moved, the `USE` through `maxDurationOr`, `cut` read from the call's own cause, what the loop does with `cut`). Criteria 6a and 7.

Files:
- Modify: `collect/collect.go` (`runUnit`, the loop)
- Modify test: `collect/unit_live_test.go` (`TestLiveAUnitLeavesNoTempTable` calls `runUnit`)
- Test: `collect/maxduration_live_test.go`

Interfaces:
- Produces: `func runUnit(ctx, bound context.Context, conn *sql.Conn, o Options, m *Manifest, rw *runWriter, s Script, u DatabaseFolder, watch *blockingWatch, spid int) (cut bool, err error)`. `cut` is true when one of the four server calls (first reset, `USE`, `QueryContext`, `ReadResultSets`) failed and `context.Cause` of that call's own context was `errMaxDurationReached` at the call's return.

- [ ] Step 1: tests.

In `collect/unit_live_test.go`: `uerr := runUnit(ctx, conn, ...)` becomes `_, uerr := runUnit(ctx, ctx, conn, ...)`.

Appended to `collect/maxduration_live_test.go`:

```go
// Criterion 6a. A unit still running at the bound is stopped; its error names
// the bound once and keeps the driver's words; the next unit is skipped; the
// unit cut by the bound is not a failure of the run's own.
func TestLiveMaxDurationStopsTheRunningUnit(t *testing.T) {
	o := maxDurRun(t, context.Background(), maxDurWait, t.TempDir(), time.Now(), 2*time.Second, nil)
	msg := ""
	if len(o.m.Errors) == 1 {
		msg = o.m.Errors[0].Message
	}
	const pre = "stopped when the collection reached its maximum duration of 0m02s (2 s): "
	if !strings.HasPrefix(msg, pre) || strings.Count(msg, "stopped when") != 1 {
		t.Errorf("the unit's error %q, want it to begin %q, once", msg, pre)
	}
	if o.m.Run.Cancelled {
		t.Error("cancelled is set")
	}
	matches, _ := filepath.Glob(filepath.Join(o.m.Config["output_dir"], "*", "10.system", "901.a*"))
	if len(matches) != 0 {
		t.Errorf("the stopped unit left files: %v", matches)
	}
	if len(o.m.Skipped) != 1 || o.m.Skipped[0].Reason != maxDurationSkipReason(2*time.Second) {
		t.Errorf("skipped %+v", o.m.Skipped)
	}
	if len(o.rec.skips) != 1 || !o.rec.skips[0].MaxDuration {
		t.Errorf("skips told: %+v", o.rec.skips)
	}
	if nl := noteLines(o.progress); len(nl) != 1 || nl[0] != "note: the collection reached its maximum duration of 0m02s (2 s); 1 collector was not started, and the one running then was stopped" {
		t.Errorf("note lines %q", nl)
	}
	if strings.Contains(o.progress, "connection lost") {
		t.Error("connection lost printed")
	}
	if v := o.verdict(t); v != (Verdict{MaxDurationReached: true, Failed: false, Collected: 0}) {
		t.Errorf("verdict %+v, want {MaxDurationReached: true}", v)
	}
	if o.code != 2 || o.m.Run.DurationSec < 2 {
		t.Errorf("exit %d, duration_sec %d; want 2 and at least 2", o.code, o.m.Run.DurationSec)
	}
	if took := o.rec.firstDone.Sub(o.began); o.rec.firstDone.IsZero() || took > 12*time.Second {
		t.Errorf("the first unit came back %s after the start, want within 12 s", took)
	}
}

// Criterion 7. runUnit with a bound already passed is cut at its first call,
// the session reset, and writes nothing. The reset is told to USE a database
// that does not exist: run on the run's context instead of the unit's, it
// would reach the server and fail with 911 rather than with the bound.
func TestLiveMaxDurationCutsTheFirstReset(t *testing.T) {
	cfg := maxDurConfig(t, t.TempDir(), 2*time.Hour)
	ctx := context.Background()
	db, err := Open(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	unitCfg := cfg
	unitCfg.Database = "ZzMaxDurMissing"
	bound, cancelBound := context.WithDeadlineCause(ctx, time.Now().Add(-time.Second), errMaxDurationReached)
	defer cancelBound()
	s := Script{Path: "10.system/901.a.sql", TimeoutSec: 60, SQL: maxDurSelect,
		Results: []ResultSpec{{Name: "root", Shape: ShapeObject}}}
	dir := t.TempDir()
	m, rw := &Manifest{}, newRunWriter(dir, 1<<20)
	cut, uerr := runUnit(ctx, bound, conn, Options{Config: &unitCfg}, m, rw, s, DatabaseFolder{}, nil, 0)
	if !cut {
		t.Error("cut is false")
	}
	if uerr == nil || !strings.HasPrefix(uerr.Error(), "stopped when the collection reached its maximum duration of 2h00m (7200 s): ") {
		t.Errorf("error %v, want the bound's sentence from the first reset", uerr)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 || len(m.Results) != 0 {
		t.Errorf("files %v, results %+v; want none", entries, m.Results)
	}
}
```

Check that `ShapeObject` is the constant's real name (`grep -n "ShapeObject\|ShapeArray" collect/*.go`); `unit_live_test.go` uses `ShapeArray`.

- [ ] Step 2: run; does not compile (`runUnit` returns one value).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveMaxDurationStopsTheRunningUnit|TestLiveMaxDurationCutsTheFirstReset|TestLiveAUnitLeavesNoTempTable)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement. `runUnit`:

```go
func runUnit(ctx, bound context.Context, conn *sql.Conn, o Options, m *Manifest,
	rw *runWriter, s Script, u DatabaseFolder, watch *blockingWatch, spid int) (cut bool, err error) {

	limit := o.Config.MaxDuration
	// The unit's own context, which the blocking watch cancels with a
	// *blockedError as the cause, and which inherits the bound's deadline and
	// cause. The run's context stays untouched, so recordUnitFailure files a
	// watch cancellation or a bound stop as a failed unit and never as an
	// operator stop.
	unitCtx, unitCancel := context.WithCancelCause(bound)
	defer unitCancel(nil)

	// The first reset runs on the unit's context, so that the bound can cut
	// it, and holds its own deadline context so that its cause can be read.
	// cut is read from that context's first cause, before its cancel, which
	// would otherwise set the cause to context.Canceled.
	dctx, dcancel := deadline(unitCtx, o.Config)
	rerr := ResetSession(dctx, conn, o.Config.Database)
	if rerr != nil {
		cut = context.Cause(dctx) == errMaxDurationReached
		rerr = maxDurationOr(ctx, dctx, limit, rerr)
	}
	dcancel()
	if rerr != nil {
		return cut, rerr
	}
```

(the `leave` closure, the deferred function and `blocked` stay as they are; the deferred function sees the named result `cut`, which Task 18 uses.) The `USE`:

```go
		uctx, ucancel := deadline(unitCtx, o.Config)
		_, uerr := conn.ExecContext(uctx, "USE "+quoteName(u.Name)+";")
		if uerr != nil {
			cut = context.Cause(uctx) == errMaxDurationReached
			uerr = maxDurationOr(ctx, uctx, limit, uerr)
		}
		ucancel()
		if uerr != nil {
			return cut, blocked(uerr)
		}
```

The query and the read:

```go
	rows, err := conn.QueryContext(qctx, s.SQL, args...)
	if err != nil {
		cut = context.Cause(qctx) == errMaxDurationReached
		return cut, blocked(outOfTime(ctx, qctx, timeout, limit, knob, err))
	}
	defer rows.Close()

	sets, err := ReadResultSets(rows, s.Results)
	if err != nil {
		cut = context.Cause(qctx) == errMaxDurationReached
		return cut, blocked(outOfTime(ctx, qctx, timeout, limit, knob, err))
	}
```

Every later `return X` in `runUnit` (the writer not registered, the encoder error, the write error, the final `return nil`) becomes `return false, X`: a file write failing after the bound is a disk error, not a cut. `outOfTime` is not wrapped again by `maxDurationOr` on the query path: one classification per call.

In the loop, the two lines `err := runUnit(ctx, conn, o, m, rw, s, target, watch, spid)` and `pause("after a unit", bound)` (the second added by Task 14) become the block below, which carries the same `pause` call; replace both lines, or the hook runs twice per unit where the spec defines one point:

```go
		cut, err := runUnit(ctx, bound, conn, o, m, rw, s, target, watch, spid)
		pause("after a unit", bound)
		// The fact, from the call the bound stopped, never from the error's
		// words: a SQL Server number returned after the bound, or a stop
		// landing while the driver returns, leaves cut true.
		if cut {
			m.Run.MaxDurationReached = true
			stoppedByBound = true
		}
```

and `exit = code` / `earned = code` become:

```go
		// A unit the bound cut is not a failure of the run's own: settleRun
		// makes the run 2 for the bound, and exit stays the record of what
		// the run earned, which the wizard reads.
		if !cut {
			exit = code
			earned = code
		}
```

- [ ] Step 4: run step 2's command: `top-level passes: 3`. Then `-run '^TestLiveMaxDuration'`: 11 passes; and the non-live suite.

- [ ] Step 5: breaks (criterion 6a's "Fails when").

| Change | Test that must fail | Prediction |
| --- | --- | --- |
| In `outOfTime`, delete the cause test | `TestLiveMaxDurationStopsTheRunningUnit` (the message names `@timeout`) | red |
| `finish` builds the verdict with `MaxDurationReached: false` | same (verdict) | red |
| Wrap the query's error again: `maxDurationOr(ctx, qctx, limit, outOfTime(...))` | same (`maxDurationOr` finds the bound's cause and no SQL number, and prefixes a second sentence) | red (the sentence counted twice) |
| Delete `if !cut` (a cut unit sets exit) | same (`Failed` true) | red |
| `Failed: code != 0` in `finish` | same (`Failed` true) | red: the break Task 11 could not make |
| `context.WithCancelCause(ctx)` for the unit (not `bound`) | same (the 1-minute `WAITFOR` runs out its minute; the first unit returns after 12 s) | red |
| The first reset back on `resetWithDeadline(ctx, ...)` before the unit's context | `TestLiveMaxDurationCutsTheFirstReset` (911 instead of the bound) | red |
| Delete `m.Run.MaxDurationReached = true` in the `if cut` | predicted green here: the second unit's skip sets the flag in 6a. Task 17's `TestLiveMaxDurationThenAStopWhileTheDriverReturns` catches it. Report it. | green |
| Read the `USE`'s cause after `ucancel()` | no test reaches the `USE` (instance units only): predicted green; the spec has no live test for the `USE` path. The review checks the order by reading. | green |

- [ ] Step 6: commit.

```bash
git add collect/collect.go collect/unit_live_test.go collect/maxduration_live_test.go
git commit -m "Stop the running collector at the bound

A bound that holds only when the slow collector is not running is not a
bound, so each unit's context now derives from the bound's, and the first
session reset moves under it. runUnit reports a unit cut when one of its
server calls failed and that call's own context was first cancelled by the
bound: a call stopped first by its own timeout or by the watch stays a
failure of the run's own, as the owner ruled. A cut unit is recorded as an
error with its duration but does not count as the run's failure."
```

### Task 17: the hook after a failing call

Spec: "Before the first unit" (the point `"after a failing call"`), "During a unit" (why the call's own cause). Criteria 6b and 6d.

Files:
- Modify: `collect/collect.go` (`runUnit`, four error returns)
- Test: `collect/maxduration_live_test.go`

Interfaces:
- Produces: the hook point `"after a failing call"`, called at each of the four error returns, immediately after the call returned its error and before its cause is read or the error classified.

- [ ] Step 1: tests, appended:

```go
// Criterion 6b. The bound cuts the query, then the operator presses ctrl-c
// while the driver returns, before runUnit reads the cause: both recorded,
// the unit's error dropped as a stop's, the loop broken.
func TestLiveMaxDurationThenAStopWhileTheDriverReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	pauseHook = func(point string, b context.Context) {
		if point == "after a failing call" && !called {
			called = true
			<-b.Done()
			cancel()
		}
	}
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, ctx, maxDurWait, t.TempDir(), time.Now(), 2*time.Second, nil)
	if !called {
		t.Fatal("the hook was never called")
	}
	if o.code != 2 || !o.m.Run.Cancelled || !o.m.Run.MaxDurationReached {
		t.Errorf("exit %d, cancelled %v, reached %v; want 2, true, true", o.code, o.m.Run.Cancelled, o.m.Run.MaxDurationReached)
	}
	if len(o.m.Errors) != 0 || len(o.m.Skipped) != 0 {
		t.Errorf("errors %+v, skipped %+v; want none: the stop drops the error and breaks the loop", o.m.Errors, o.m.Skipped)
	}
	if nl := noteLines(o.progress); len(nl) != 1 || !strings.HasSuffix(nl[0], "; the collector running then was stopped") {
		t.Errorf("note lines %q", nl)
	}
	if v := o.verdict(t); !v.Cancelled || !v.MaxDurationReached {
		t.Errorf("verdict %+v", v)
	}
}

// Criterion 6d. The unit's own @timeout of one second expires first; the
// bound passes before runUnit reads the cause, as it would during a long wait
// for the cancellation. The unit failed on its own; the bound cut only what
// came after.
func TestLiveMaxDurationDuringTheWaitAfterTheUnitsOwnLimit(t *testing.T) {
	called := false
	pauseHook = waitOnceAt("after a failing call", &called)
	defer func() { pauseHook = nil }()
	o := maxDurRun(t, context.Background(), maxDurOwnTimeout, t.TempDir(), time.Now(), 3*time.Second, nil)
	if !called {
		t.Fatal("the hook was never called")
	}
	if o.code != 2 || !o.m.Run.MaxDurationReached {
		t.Errorf("exit %d, reached %v; want 2, true", o.code, o.m.Run.MaxDurationReached)
	}
	if len(o.m.Errors) != 1 || !strings.HasPrefix(o.m.Errors[0].Message, "still running when @timeout of 1s expired") {
		t.Errorf("errors %+v; want the unit's own limit named", o.m.Errors)
	}
	if len(o.m.Skipped) != 1 || o.m.Skipped[0].Reason != maxDurationSkipReason(3*time.Second) {
		t.Errorf("skipped %+v", o.m.Skipped)
	}
	if nl := noteLines(o.progress); len(nl) != 1 || !strings.HasSuffix(nl[0], "1 collector was not started") {
		t.Errorf("note lines %q, want no clause about a stopped collector", nl)
	}
	if v := o.verdict(t); !v.MaxDurationReached || !v.Failed {
		t.Errorf("verdict %+v, want MaxDurationReached and Failed", v)
	}
}
```

- [ ] Step 2: run; both fail (`the hook was never called`).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveMaxDurationThenAStopWhileTheDriverReturns|TestLiveMaxDurationDuringTheWaitAfterTheUnitsOwnLimit)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement. In `runUnit`, as the first statement inside each of the four `if ... != nil {` error branches of Task 16 (the reset's `if rerr != nil`, the `USE`'s `if uerr != nil` that reads the cause, the `QueryContext` and the `ReadResultSets` branches), before the line that reads the cause:

```go
		pause("after a failing call", bound)
```

with one comment at the first: "A test seam: a test waits here on bound.Done(), which places the bound, or a stop after it, between the call's return and the read of its cause. A server call added to runUnit later needs this point and the read of its cause as well."

- [ ] Step 4: rerun: `top-level passes: 2`; then `-run '^TestLiveMaxDuration'`: 13 passes.

- [ ] Step 5: breaks.

| Change | Test that must fail |
| --- | --- |
| In the loop, `cut = err != nil && strings.HasPrefix(err.Error(), "stopped when")` after `runUnit` (the flag from the error) | `TestLiveMaxDurationThenAStopWhileTheDriverReturns` (the run's context is dead at classification, `outOfTime` returns the bare error; flag, note and verdict wrong) |
| In `runUnit`, `cut = boundReached(bound)` at the query's return (version 5's rule) | `TestLiveMaxDurationDuringTheWaitAfterTheUnitsOwnLimit` (`Failed` false, the note claims a stopped collector) |
| Delete `m.Run.MaxDurationReached = true` in the loop's `if cut` (Task 16's green break) | `TestLiveMaxDurationThenAStopWhileTheDriverReturns` (no skip follows, so nothing else sets the flag) |
| Take `cut` in the loop from `boundReached(bound)` when `err != nil` | `TestLiveMaxDurationAfterAFailureOfTheUnitsOwn` (Task 14: `Failed` false) |

- [ ] Step 6: commit.

```bash
git add collect/collect.go collect/maxduration_live_test.go
git commit -m "Let a test place the bound between a call's return and its cause

The flag must come from the call the bound stopped, not from the words of
its error, and the case that tells them apart is a stop landing while the
driver returns. Without a seam inside runUnit no test can put the stop
there, and the version of this criterion that used the seam after the unit
stayed green when the flag was taken from the error."
```

### Task 18: the watch's record after a bound stop

Spec: "During a unit" (what a stopped unit costs: the watch's record). Criterion 15.

Files:
- Modify: `collect/watch.go` (new `recordWatchOutcome`), `collect/collect.go` (`runUnit`'s deferred function)
- Test: `collect/watch_test.go`

Interfaces:
- Produces: `func recordWatchOutcome(m *Manifest, script, target string, worst waitSample, fired, cut bool, err error)`, the deferred switch of `runUnit` moved into a function so that its cases are tested without a server. `runUnit`'s defer keeps `disarmed`, `leave` and `AddBlockedWait`, then calls it.

- [ ] Step 1: test in `collect/watch_test.go` (import the driver as `collect_test.go` does):

```go
// The deferred switch of runUnit, case by case. A unit stopped at the bound
// may hold locks while the driver waits for the cancellation, and the watch
// can reach its limit then: that is neither "had already read its rows" nor
// "under the limit". The case reads cut, not the error, so a cut unit whose
// error kept a SQL Server number gets the same sentence.
func TestTheWatchsRecordAfterABoundStop(t *testing.T) {
	worst := waitSample{Session: 70, WaitType: "LCK_M_S", Waited: 5300 * time.Millisecond, Resource: "KEY: 5:1 (a)"}
	const unit = "70.schema/055.page-density.sql on SALESDB: session 70 had been waiting on this collector for 5.3 s (LCK_M_S, KEY: 5:1 (a))"
	stopped := unit + "; the collector was being stopped at the collection's maximum duration when the blocking watch reached its 5s limit"
	for _, c := range []struct {
		name       string
		fired, cut bool
		err        error
		warning    string
		cancelled  int
	}{
		{"cut, the watch fired", true, true, context.DeadlineExceeded, stopped, 0},
		{"cut with a SQL error number, the watch fired", true, true, mssql.Error{Number: 1222, Message: "Lock request time out period exceeded."}, stopped, 0},
		{"not cut, the watch fired", true, false, nil, unit + "; the collector had already read its rows, and the waiter was released when the session left the database", 0},
		{"a wait under the limit", false, false, nil, unit + ", under the blocking watch's 5s limit", 0},
		{"cancelled by the watch", true, false, &blockedError{Sample: worst}, "", 1},
	} {
		m := &Manifest{}
		recordWatchOutcome(m, "70.schema/055.page-density.sql", "SALESDB", worst, c.fired, c.cut, c.err)
		got := strings.Join(m.Warnings, "|")
		if got != c.warning || m.BlockingWatch.CancelledUnits != c.cancelled {
			t.Errorf("%s: warnings %q, cancelled %d; want %q, %d", c.name, got, m.BlockingWatch.CancelledUnits, c.warning, c.cancelled)
		}
	}
}
```

Check the field name of the manifest's warnings (`grep -n "Warnings" collect/manifest.go`) and that `m.warn` appends to it.

- [ ] Step 2: run; does not compile.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ -run '^TestTheWatchsRecordAfterABoundStop$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined' "$LOG"
```

- [ ] Step 3: implement. In `collect/watch.go`:

```go
// recordWatchOutcome is runUnit's account of what the watch saw while the
// unit was armed, apart from the blocked wait itself. The case of a unit cut
// by the bound comes after the watch's own cancellation and before fired: the
// watch can reach its limit while the driver waits for the cancellation of a
// unit the bound stopped, and fired's sentence (the rows already read) is
// false for it, as is the worst wait's (under the limit). It is reached only
// when the bound came first: a watch that cancelled first leaves its
// *blockedError as the cause, and the first case files it. It reads cut and
// not the error, so a cut unit whose error kept a SQL Server number gets the
// same sentence.
func recordWatchOutcome(m *Manifest, script, target string, worst waitSample, fired, cut bool, err error) {
	var be *blockedError
	switch {
	case errors.As(err, &be):
		m.BlockingWatch.CancelledUnits++
	case cut && fired:
		m.warn(fmt.Sprintf(
			"%s on %s: %s; the collector was being stopped at the collection's maximum duration when the blocking watch reached its %s limit",
			script, orInstance(target), worst, watchCancelAfter))
	case fired:
		m.warn(fmt.Sprintf(
			"%s on %s: %s; the collector had already read its rows, and the waiter was released when the session left the database",
			script, orInstance(target), worst))
	case worst.seen():
		m.warn(fmt.Sprintf(
			"%s on %s: %s, under the blocking watch's %s limit",
			script, orInstance(target), worst, watchCancelAfter))
	}
}
```

In `runUnit`'s deferred function, replace the `switch {...}` with `recordWatchOutcome(m, s.Path, u.Name, worst, fired, cut, err)`; the `var be *blockedError` stays for `incidentOf(..., errors.As(err, &be))`.

- [ ] Step 4: rerun: 1 pass; whole `collect` suite (the watch tests).

- [ ] Step 5: breaks.

| Change | Prediction |
| --- | --- |
| Move `case cut && fired` after `case fired` | red, rows 1 and 2 |
| Replace `cut && fired` by `fired && sqlErrorNumber(err) == 0 && err != nil` (reads the error) | red, row 2 |
| In `runUnit`'s defer, pass `false` instead of `cut` | no offline test reaches the defer, and the lab cannot produce a watch firing during a bound stop on demand: predicted green. The review reads the call. This is the propagation the spec lists as "reasoned only" ("The watch first, then the bound during the wait"). |

- [ ] Step 6: commit.

```bash
git add collect/watch.go collect/collect.go collect/watch_test.go
git commit -m "Say what the watch saw when the bound was stopping a collector

A unit stopped at the bound can still hold locks while the driver waits
for the cancellation, and the watch can reach its limit in that interval.
Both existing sentences were false for it: the rows had not been read, and
the wait was not under the limit. The switch moves into a function so its
cases can be tested without a server, and gains that case."
```

### Task 19: the command line's gauge

Spec: "How a bounded run is recorded" (the observer, the command line's gauge). Criterion 13, the gauge.

Files:
- Modify: `cmd/sql-auditor/progress.go` (`UnitDone`)
- Test: `cmd/sql-auditor/progress_test.go`

- [ ] Step 1: test:

```go
// A bound that fires early skips hundreds of units for one reason. The gauge
// counts them to its total without a line each; the note after the loop is
// the one line that says it. A skip of the watch still gets its line.
func TestTheGaugeCountsTheBoundsSkipsWithoutPrintingThem(t *testing.T) {
	for _, tty := range []bool{true, false} {
		var b strings.Builder
		o := newProgress(&b, tty, func() int { return 80 }, fixedClock())
		o.Planned(200)
		o.UnitStarted("10.system/001.a.sql", "")
		o.UnitDone("10.system/001.a.sql", "", 10, time.Second, nil)
		o.UnitDone("20.databases/010.b.sql", "SALESDB", 0, 0,
			&collect.UnitSkipped{Reason: "the blocking watch cancelled 70.schema/055.page-density.sql on this database"})
		for i := 0; i < 198; i++ {
			o.UnitDone(fmt.Sprintf("80.workload/%03d.c.sql", i), "SALESDB", 0, 0,
				&collect.UnitSkipped{Reason: "the collection reached its maximum duration of 2h00m (7200 s) before this collector started", MaxDuration: true})
		}
		o.Finished(collect.Verdict{MaxDurationReached: true})
		out := b.String()
		if n := strings.Count(out, "maximum duration"); n != 0 {
			t.Errorf("tty %v: %d lines about the bound's skips, want none", tty, n)
		}
		// The non-tty [n/N] line names the unit and never the reason, so the
		// count above cannot see a bound skip printed as progress.
		if n := strings.Count(out, "80.workload/"); n != 0 {
			t.Errorf("tty %v: %d lines name a unit the bound skipped, want none", tty, n)
		}
		if strings.Count(out, "-- ") != 1 || !strings.Contains(out, "the blocking watch cancelled") {
			t.Errorf("tty %v: the watch's skip lost its line:\n%q", tty, out)
		}
		if o.done != o.planned {
			t.Errorf("tty %v: counted %d of %d", tty, o.done, o.planned)
		}
	}
}
```

(`o.done` and `o.planned` are the unexported fields `progress` keeps; check their names in `progress.go`. Add `fmt` to the test imports if absent.)

- [ ] Step 2: run; fails (198 lines).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./cmd/sql-auditor/ -run '^TestTheGaugeCountsTheBoundsSkipsWithoutPrintingThem$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement. In `UnitDone`, the skip branch becomes:

```go
	if errors.As(err, &skip) {
		// Kept on screen like a failure, because it is news: the run chose,
		// while running, not to run something the plan announced. Except the
		// bound's skips, which come by the hundred for one reason: counted,
		// and said once by the note Run prints after the loop.
		if !skip.MaxDuration {
			p.clear()
			fmt.Fprintf(p.out, "-- %s: %v\n", unitLabel(script, database), err)
		}
	} else if err != nil {
```

- [ ] Step 4: rerun: 1 pass; `rtk proxy go test ./cmd/sql-auditor/ -count=1`.

- [ ] Step 5: breaks.

| Change | Prediction |
| --- | --- |
| Delete the `if !skip.MaxDuration` test | red on both rows: 198 lines about the bound |
| Make the bound's skip fall through to the `!p.tty` branch: `if errors.As(err, &skip) && !skip.MaxDuration { ... } else if err != nil && skip == nil { ... } else if !p.tty { ... }` | red on the non-tty row only, and only through the `80.workload/` count: the `[n/N]` line carries the label, the elapsed time and the bytes, never the reason, so the `maximum duration` count stays 0. Measured on 5 October: `tty false: 198 lines name a unit the bound skipped` |

- [ ] Step 6: commit.

```bash
git add cmd/sql-auditor/progress.go cmd/sql-auditor/progress_test.go
git commit -m "Count the bound's skips on the gauge without a line each

Fed the skips of a bound that fired early, the gauge printed one permanent
line per unit, two hundred of them above the one note that explains them.
It now counts them to its total in silence and keeps a line for the
watch's skips, which are rare and each worth reading."
```

### Task 20: the wizard's counts

Spec: "How a bounded run is recorded" (the observer in the wizard, the summary line, `CollectedUnits`). Criterion 13, the wizard.

Files:
- Modify: `tui/state.go` (`State`), `tui/observer.go` (`finishedEvent.apply`, `unitDoneEvent.apply`), `tui/render.go` (`summaryLine`)
- Modify tests: `tui/render_test.go` (`collectingState` and the two final-screen tests that set `DoneUnits` for the summary)
- Test: `tui/observer_test.go`

Interfaces:
- Produces: `State.MaxDurationReached bool`, `State.RunFailed bool`, `State.CollectedUnits int`, set by `finishedEvent` from the `Verdict`.

- [ ] Step 1: test in `tui/observer_test.go`:

```go
// The summary line counts what was collected, which only the run knows: a
// unit the operator interrupted reaches the wizard as a UnitDone with no
// error. The bound's skips are counted as skips without a note each, so they
// do not push the stopped unit's error off the six notes kept.
func TestTheWizardCountsWhatWasCollectedNotWhatWasDone(t *testing.T) {
	ch := make(chan event, 400)
	o := observer{ch: ch}
	o.Planned(289)
	for i := 0; i < 90; i++ {
		o.UnitDone(fmt.Sprintf("10.system/%03d.a.sql", i), "", 10, time.Second, nil)
	}
	o.UnitDone("70.schema/055.page-density.sql", "SALESDB", 0, 2*time.Hour,
		errors.New("stopped when the collection reached its maximum duration of 2h00m (7200 s): context deadline exceeded"))
	for i := 0; i < 198; i++ {
		o.UnitDone(fmt.Sprintf("80.workload/%03d.b.sql", i), "SALESDB", 0, 0,
			&collect.UnitSkipped{Reason: "the collection reached its maximum duration of 2h00m (7200 s) before this collector started", MaxDuration: true})
	}
	o.Finished(collect.Verdict{MaxDurationReached: true, Collected: 90})
	close(ch)
	var f frames
	end, _ := drive(ch, f.draw, fixedSize(80, 24), State{Step: StepCollecting})
	lines := renderDone(end, testWidth)
	contains(t, lines, "90 collected, 198 skipped, 1 error, 0 permissions denied")
	for _, n := range end.Notes {
		if strings.Contains(n, "before this collector started") {
			t.Errorf("a bound skip became a note: %q", n)
		}
	}

	ch = make(chan event, 8)
	o = observer{ch: ch}
	o.Planned(3)
	o.UnitDone("10.system/001.a.sql", "", 10, time.Second, nil)
	o.UnitDone("10.system/002.b.sql", "", 10, time.Second, nil)
	o.UnitDone("10.system/003.c.sql", "", 0, time.Second, nil) // interrupted: recordUnitFailure swallowed the stop
	o.Finished(collect.Verdict{Cancelled: true, Collected: 2})
	close(ch)
	end, _ = drive(ch, f.draw, fixedSize(80, 24), State{Step: StepCollecting})
	contains(t, renderDone(end, testWidth), "2 collected")
}
```

(Add `errors`, `fmt`, `strings` and `collect` to the imports of `observer_test.go` as needed.)

- [ ] Step 2: run; the new test fails on `289 collected` (it references no new field, so it compiles before the change).

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./tui/ -run '^(TestTheWizardCountsWhatWasCollectedNotWhatWasDone|TestDoneCountsDeniedPermissionsFromTheVerification|TestDoneCarriesTheNotesTheCollectionScreenShowed|TestACancelledRunSaysTheArchiveIsPartial)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

Expected before: `top-level passes: 3` (the three existing tests), one `FAIL`.

- [ ] Step 3: implement.

`tui/state.go`, after `Cancelled bool`:

```go
	// The rest of the run's verdict, from finishedEvent. MaxDurationReached is
	// set when the bound cut the run; RunFailed when the run had failed on its
	// own before the stop or the bound (a lint error, a collector failing for
	// its own reason); CollectedUnits is the run's count of units that
	// returned a result, which no event can give: an interrupted unit arrives
	// as a UnitDone with no error. The gauge keeps DoneUnits against Units.
	MaxDurationReached bool
	RunFailed          bool
	CollectedUnits     int
```

`tui/observer.go`: `finishedEvent.apply`:

```go
func (e finishedEvent) apply(s State) State {
	s.Cancelled = e.v.Cancelled
	s.MaxDurationReached = e.v.MaxDurationReached
	s.RunFailed = e.v.Failed
	s.CollectedUnits = e.v.Collected
	return s
}
```

and in `unitDoneEvent.apply`, the skip branch:

```go
	if errors.As(e.err, &skip) {
		s.SkippedCount++
		// The bound's skips come by the hundred for one reason, and six notes
		// are kept: a note each would push the stopped unit's error off the
		// screen. The last screen says the bound once.
		if !skip.MaxDuration {
			s.Notes = note(s.Notes, status("skipped")+where(e.script, e.database)+": "+skip.Reason)
		}
		return s
	}
```

`tui/render.go`, `summaryLine`: `fmt.Sprintf("%d collected", s.CollectedUnits)`.

The assertion moves with the count: `collectingState()` gains `CollectedUnits: 147`; `TestDoneCountsDeniedPermissionsFromTheVerification` sets `s.CollectedUnits = 219` beside `DoneUnits`; `TestDoneCarriesTheNotesTheCollectionScreenShowed` sets `s.CollectedUnits = 147`. These tests keep asserting the summary line; only its source moved.

- [ ] Step 4: rerun: `top-level passes: 4`; `rtk proxy go test ./tui/ -count=1`.

- [ ] Step 5: breaks.

| Change | Prediction |
| --- | --- |
| `summaryLine` prints `s.DoneUnits` again | red (`289 collected`) |
| `unitDoneEvent.apply` raises `CollectedUnits` on a nil error, and `finishedEvent` leaves it alone | red (`3 collected`) |
| Drop the `!skip.MaxDuration` test | red (a bound skip among the notes) |

- [ ] Step 6: commit.

```bash
git add tui/state.go tui/observer.go tui/render.go tui/observer_test.go tui/render_test.go
git commit -m "Count collected units on the wizard's last screen from the run

The summary line printed every unit done as collected, skips and errors
included; with a bound that is the whole tail of the plan, on the screen
the operator reads before mailing the archive. The count now comes from the
run's verdict, the only place that knows an interrupted unit from a
collected one, and the bound's skips are counted without a note each."
```

### Task 21: the wizard's exit and last screen

Spec: "Exit code and the previous run of the day" (how the wizard knows, the order of the three rules), "How a bounded run is recorded" (`renderDone`'s table). Criterion 14.

Files:
- Modify: `tui/loop.go` (`coded`, `loop`, `panicEvent.exitStatus`), `tui/run.go` (`collectDoneEvent.exitStatus`), `tui/render.go` (`renderDone`)
- Modify tests: `tui/loop_test.go` (`runFinished.exitStatus`), `tui/run_test.go` (`TestACancelledCollectionExitsZero`)
- Test: `tui/run_test.go`

Interfaces:
- Produces: `type coded interface{ exitStatus(s State) int }`; `loop` calls `c.exitStatus(s)` with the state before the event is applied.

- [ ] Step 1: tests in `tui/run_test.go`:

```go
// The wizard's exit after a bound, driven through the loop with every event
// produced by the wizard's own observer, in the order Run produces them, then
// the collectDoneEvent. A hand-built finishedEvent passes with an
// observer.Finished that drops the bound; these do not. The first and third
// rows also render the state the loop ended on, so that the last screen is
// read from what the events built and not from a State written by hand.
func TestTheWizardsExitAfterABound(t *testing.T) {
	const zip = `C:\out\SQL01-2026-10-05.zip`
	boundErr := errors.New("the collection reached its maximum duration of 1m00s (60 s) before the first collector: nothing was collected")
	stopErr := errors.New("stopped before the first collector: nothing was collected")
	skip := &collect.UnitSkipped{Reason: "the collection reached its maximum duration of 1m00s (60 s) before this collector started", MaxDuration: true}
	for _, c := range []struct {
		name  string
		units []error // one UnitDone per entry
		v     collect.Verdict
		done  collectDoneEvent
		want  int
		first string // a line the last screen must carry, "" for no check
	}{
		{"bound, collected one", []error{nil}, collect.Verdict{MaxDurationReached: true, Collected: 1},
			collectDoneEvent{code: 2, zipPath: zip, zipBytes: 1024}, 0,
			"Collection stopped at its maximum duration. This archive is partial:"},
		{"bound, failed before it", []error{nil}, collect.Verdict{MaxDurationReached: true, Failed: true, Collected: 1},
			collectDoneEvent{code: 2, zipPath: zip, zipBytes: 1024}, 2, ""},
		{"bound before the run folder", nil, collect.Verdict{MaxDurationReached: true},
			collectDoneEvent{code: 2, err: boundErr}, 2,
			"Collection stopped at its maximum duration. No archive was written by this run."},
		{"bound, then a stop, before the run folder", nil, collect.Verdict{MaxDurationReached: true, Cancelled: true},
			collectDoneEvent{code: 2, err: boundErr, ctxCancelled: true}, 2, ""},
		{"bound, three skips, nothing collected", []error{skip, skip, skip}, collect.Verdict{MaxDurationReached: true},
			collectDoneEvent{code: 2, zipPath: zip, zipBytes: 1024}, 2, ""},
		{"a stop alone", nil, collect.Verdict{Cancelled: true},
			collectDoneEvent{code: 2, err: stopErr, ctxCancelled: true}, 0, ""},
	} {
		ch := make(chan event, 16)
		o := observer{ch: ch}
		o.Planned(len(c.units))
		for i, err := range c.units {
			o.UnitDone(fmt.Sprintf("10.system/%03d.a.sql", i), "", 0, time.Second, err)
		}
		o.Finished(c.v)
		ch <- c.done
		ch <- key(screen.KeyEnter)
		close(ch)
		var f frames
		final, code := drive(ch, f.draw, fixedSize(80, 24), State{Step: StepCollecting})
		if code != c.want {
			t.Errorf("%s: exit %d, want %d", c.name, code, c.want)
		}
		// The enter key took the wizard from StepDone to StepQuit and left the
		// rest of the state as the events built it; renderDone draws the last
		// screen from that state whatever its Step.
		if c.first != "" && !strings.Contains(joined(renderDone(final, testWidth)), c.first) {
			t.Errorf("%s: the last screen does not carry %q:\n%s", c.name, c.first, joined(renderDone(final, testWidth)))
		}
	}
}

// The first line of the last screen, with and without an archive, when the
// bound cut the run: the bound before the stop, since it came first.
func TestTheWizardsLastScreenAfterABound(t *testing.T) {
	for _, c := range []struct {
		zip  string
		want string
	}{
		{`C:\out\SQL01-2026-10-05.zip`, "Collection stopped at its maximum duration. This archive is partial:"},
		{"", "Collection stopped at its maximum duration. No archive was written by this run."},
	} {
		for _, cancelled := range []bool{false, true} {
			s := State{Step: StepDone, ZipPath: c.zip, MaxDurationReached: true, Cancelled: cancelled}
			lines := Render(s, testWidth, 0)
			contains(t, lines, c.want)
			absent(t, lines, "Send this file")
		}
	}
}
```

Add to the imports of `tui/run_test.go` what these use and it lacks (`errors`, `fmt`, `strings`, `time`, `collect`, `tui/screen`); `frames`, `drive`, `fixedSize` and `key` are in `tui/loop_test.go`, `joined`, `contains`, `absent` and `testWidth` in `tui/render_test.go`, all in the same package.

`TestACancelledCollectionExitsZero`: `e.exitStatus()` becomes `e.exitStatus(State{})` (twice), and `(collectDoneEvent{code: 2}).exitStatus()` becomes `.exitStatus(State{})`. In `tui/loop_test.go`: `func (e runFinished) exitStatus(State) int { return e.code }`.

- [ ] Step 2: run; does not compile until the interface changes. After the signature change alone (step 3, first part: `coded`, `loop`, `panicEvent`, and `collectDoneEvent.exitStatus(State)` keeping today's body, which asks only `ctxCancelled`), these fail: row "bound, collected one" (exit 2, want 0, and its screen), row "bound before the run folder" (its screen only, exit 2 either way), row "bound, then a stop, before the run folder" (exit 0, want 2), and `TestTheWizardsLastScreenAfterABound`. Rows "bound, failed before it", "bound, three skips" and "a stop alone" already pass on today's body: their expected codes are Run's code or the stop's 0. The breaks of step 5 are what make those three rows fail.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./tui/ -run '^(TestTheWizardsExitAfterABound|TestTheWizardsLastScreenAfterABound|TestACancelledCollectionExitsZero|TestLoopReturnsTheExitCodeTheRunReported)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
```

- [ ] Step 3: implement.

`tui/loop.go`: `type coded interface{ exitStatus(s State) int }`, with the comment extended: "s is the state as it stands before the event is applied, which holds the run's verdict: finishedEvent and collectDoneEvent come from the one goroutine that calls collect.Run, on one channel, and the first is applied before the second." In `loop`: `code = c.exitStatus(s)`. `func (e panicEvent) exitStatus(State) int { return 2 }`.

`tui/run.go`:

```go
// exitStatus decides the wizard's exit code from Run's code and the run's
// verdict, which finishedEvent put into s before this event arrived.
//
// The bound first, since when both are set it came first. A run the bound cut
// exits 0 only when it produced an archive (Run returned no error), collected
// something, and had not failed on its own before the bound: the operator
// accepted a partial archive in advance by setting the bound, and the last
// screen calls it partial. Otherwise Run's code, 2 for every run the bound
// cut, so that a scheduler wrapping the wizard does not record success for a
// run with nothing to send. Then the operator's stop, 0 as before: the
// operator who stops has read the screen, the bound has no one watching.
func (e collectDoneEvent) exitStatus(s State) int {
	if s.MaxDurationReached {
		if e.err == nil && !s.RunFailed && s.CollectedUnits > 0 {
			return 0
		}
		return e.code
	}
	if e.ctxCancelled {
		return 0
	}
	return e.code
}
```

`tui/render.go`, `renderDone`'s switch:

```go
	switch {
	case s.ZipPath == "":
		switch {
		case s.MaxDurationReached:
			out = append(out, pad+"Collection stopped at its maximum duration. No archive was written by this run.")
		case s.Cancelled:
			out = append(out, pad+"Collection stopped. No archive was written by this run.")
		default:
			out = append(out, pad+"No archive was written by this run.")
		}
	case s.MaxDurationReached:
		out = append(out, pad+"Collection stopped at its maximum duration. This archive is partial:")
	case s.Cancelled:
		out = append(out, pad+"Collection stopped. This archive is partial:")
	default:
		out = append(out, pad+"Send this file to whoever requested the audit:")
	}
```

(keeping the existing comments).

- [ ] Step 4: rerun: `top-level passes: 4`; `rtk proxy go test ./tui/ -count=1`.

- [ ] Step 5: breaks (criterion 14's "Fails when").

| Change | Row that must fail |
| --- | --- |
| `finishedEvent.apply` drops `RunFailed` | "bound, failed before it" exits 0 |
| `finishedEvent.apply` drops `MaxDurationReached` | "bound, collected one" (exits 2, want 0) and "bound, then a stop, before the run folder" (exits 0, want 2), plus the two screen checks. "bound before the run folder" keeps exit 2, since Run's code is 2 either way. Measured on 5 October for the exit codes |
| Ask `e.ctxCancelled` before `s.MaxDurationReached` | "bound, then a stop, before the run folder" exits 0 |
| Drop `s.CollectedUnits > 0` | "bound, three skips, nothing collected" exits 0 |
| Drop `!s.RunFailed` | "bound, failed before it" exits 0 |
| Delete the bound's case from either branch of `renderDone` | `TestTheWizardsLastScreenAfterABound`, and the screen check of row "bound, collected one" (archive branch) or "bound before the run folder" (no-archive branch) |
| `collectDoneEvent.apply` leaves `ZipPath` empty | row "bound, collected one", its screen (`No archive was written`); existing tests of the last screen fail too |
| `loop` passes the state after `apply` (`c.exitStatus(e.apply(s))`) | predicted green: `collectDoneEvent.apply` does not touch the verdict fields. Report it. |

- [ ] Step 6: commit.

```bash
git add tui/loop.go tui/run.go tui/render.go tui/loop_test.go tui/run_test.go
git commit -m "Decide the wizard's exit after a bound from the run's verdict

The wizard exits 0 for a stop because the operator read the screen that
calls the archive partial. A bound is the same decision taken in advance,
but only when there is something to send and the run had not already
failed: a bound that cut the run before anything was collected, or after a
collector failed on its own, exits 2 as on the command line. The verdict
reaches the exit rule through the state, which finishedEvent fills before
collect.Run's return is applied."
```

### Task 22: the bound in `check` and on the wizard's third screen

Spec: "What check and the wizard announce". Criterion 12.

Files:
- Modify: `collect/duration.go` (`BoundLine`, `DurationBound.Against`), `collect/collect.go` (`Check`), `tui/state.go` (`State.Bound`, `State.MaxDuration`), `tui/run.go` (`initialState`), `tui/render.go` (`renderOptions`)
- Test: `collect/duration_test.go`, `collect/check_test.go`, `tui/render_test.go`

Interfaces:
- Produces: `func BoundLine(cfg *Config) string` (the sentence after the label, `""` without a bound); `func (b DurationBound) Against(limit time.Duration) string`; `State.Bound string`, `State.MaxDuration time.Duration`.

- [ ] Step 1: tests.

`collect/duration_test.go`:

```go
func TestDurationBoundAgainstTheBound(t *testing.T) {
	const pre = "bounded at 2h00m (7200 s): "
	both := []string{"70.schema/041.compression-savings.sql", "70.schema/055.page-density.sql"}
	for _, c := range []struct {
		name string
		b    DurationBound
		want string
	}{
		{"the ceiling under the bound", DurationBound{Units: 10, Ceiling: 3600 * time.Second},
			pre + "the ceiling of the units is under the bound"},
		{"the ceiling at the bound", DurationBound{Units: 10, Ceiling: 7200 * time.Second},
			pre + "the ceiling of the units is under the bound"},
		{"a bound far above the ceiling", DurationBound{Units: 289, Ceiling: 34290 * time.Second, Costly: both, CostlyUnits: 12, CostlyCeiling: 21600 * time.Second},
			"bounded at 720h00m (2592000 s): the ceiling of the units is under the bound"},
		{"costly on, the units before them above", DurationBound{Units: 301, Ceiling: 55890 * time.Second, Costly: both, CostlyUnits: 12, CostlyCeiling: 21600 * time.Second},
			pre + "the costly collectors run last; the 289 units before them: at most 9h31m (34290 s), above the bound"},
		{"costly on, the units before them under", DurationBound{Units: 20, Ceiling: 30000 * time.Second, Costly: both, CostlyUnits: 8, CostlyCeiling: 25000 * time.Second},
			pre + "the costly collectors run last; the 12 units before them: at most 1h23m (5000 s), under the bound"},
		{"no costly collector, above", DurationBound{Units: 289, Ceiling: 34290 * time.Second},
			pre + "the ceiling is above the bound; if it is reached, the collectors last in the plan are the ones not run"},
	} {
		limit := 2 * time.Hour
		if c.name == "a bound far above the ceiling" {
			limit = 720 * time.Hour
		}
		if got := c.b.Against(limit); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestBoundLineNamesTheValueAndWhereItCameFrom(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want string
	}{
		{Config{MaxDuration: 2 * time.Hour, MaxDurationFrom: ".env"},
			"MAX_DURATION at 2h00m (7200 s), from .env; no collector starts after it, and the one running then is stopped"},
		{Config{MaxDuration: 90 * time.Minute, MaxDurationFrom: "--max-duration"},
			"MAX_DURATION at 1h30m (5400 s), from --max-duration; no collector starts after it, and the one running then is stopped"},
		{Config{}, ""},
	} {
		if got := BoundLine(&c.cfg); got != c.want {
			t.Errorf("BoundLine(%s from %q) = %q, want %q", c.cfg.MaxDuration, c.cfg.MaxDurationFrom, got, c.want)
		}
	}
}
```

`collect/check_test.go` (the harness `checkConfig`, `checkCorpus`, `captureStdout` is in this file):

```go
// The bound is printed before check connects, so that a check that cannot
// reach the instance, or cannot price the plan, still shows that a bound was
// read, which is the only sign that one given through the environment was.
func TestCheckPrintsTheBoundBeforeItConnects(t *testing.T) {
	cfg := checkConfig(filepath.Join(t.TempDir(), "output"))
	cfg.MaxDuration, cfg.MaxDurationFrom = 2*time.Hour, ".env"
	out := captureStdout(t, func() {
		Check(context.Background(), Options{Config: cfg, Corpus: checkCorpus, Root: "queries"})
	})
	want := "Bound    : MAX_DURATION at 2h00m (7200 s), from .env; no collector starts after it, and the one running then is stopped\n"
	if !strings.Contains(out, want) {
		t.Errorf("no %q in\n%s", want, out)
	}
	if strings.Contains(out, "Duration, a ceiling") {
		t.Fatal("the offline check reached a ceiling: this test no longer proves the line is printed without one")
	}
}
```

`tui/render_test.go`:

```go
func TestTheThirdScreenShowsTheBound(t *testing.T) {
	v := probedVerify()
	v.Scripts = []collect.Script{{Path: "10.system/010.properties.sql", Scope: collect.ScopeInstance, TimeoutSec: 60}}
	s := State{Step: StepOptions, Verify: v, Flags: map[string]bool{},
		Bound:       collect.BoundLine(&collect.Config{MaxDuration: 2 * time.Hour, MaxDurationFrom: ".env"}),
		MaxDuration: 2 * time.Hour}
	lines := Render(s, testWidth, 0)
	contains(t, lines, "MAX_DURATION at 2h00m (7200 s)")
	contains(t, lines, "bounded at 2h00m (7200 s):")
	s.Bound, s.MaxDuration = "", 0
	absent(t, Render(s, testWidth, 0), "MAX_DURATION")
}
```

- [ ] Step 2: run; does not compile.

```bash
WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && rtk proxy go test ./collect/ ./tui/ -run '^(TestDurationBoundAgainstTheBound|TestBoundLineNamesTheValueAndWhereItCameFrom|TestCheckPrintsTheBoundBeforeItConnects|TestTheThirdScreenShowsTheBound|TestCheckPrintsTheCorpusBeforeItTouchesTheInstance|TestCheckStillWritesItsListingToStdout|TestPlannedDurationLines)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) |undefined' "$LOG"
```

- [ ] Step 3: implement.

`collect/duration.go`:

```go
// BoundLine is the bound as check and the wizard's third screen state it:
// the value written by formatCeiling, since Config keeps no spelling and
// time.Duration prints 2h as 2h0m0s; the key's name as fixed text, so the
// operator learns what to look for in .env; the provenance, because a .env
// beats an exported variable here and a bound nobody remembers setting is
// the one that will be argued about; and the rule. Empty without a bound.
func BoundLine(cfg *Config) string {
	if cfg == nil || cfg.MaxDuration <= 0 {
		return ""
	}
	return fmt.Sprintf("MAX_DURATION at %s, from %s; no collector starts after it, and the one running then is stopped",
		formatCeiling(cfg.MaxDuration), cfg.MaxDurationFrom)
}

// Against compares the ceiling with the bound and makes no forecast. When
// costly collectors are on and the whole ceiling is above the bound, it gives
// the ceiling of the units before them, since those can exceed the bound too
// and a line that named only the costly ones would mislead. It never promises
// that only costly collectors will be cut: the bound also counts the steps
// before the first unit and the resets between units, which no ceiling does.
func (b DurationBound) Against(limit time.Duration) string {
	head := "bounded at " + formatCeiling(limit) + ": "
	switch {
	case b.Ceiling <= limit:
		return head + "the ceiling of the units is under the bound"
	case len(b.Costly) > 0:
		before := b.Ceiling - b.CostlyCeiling
		side := "above"
		if before <= limit {
			side = "under"
		}
		return fmt.Sprintf("%sthe costly collectors run last; the %d units before them: at most %s, %s the bound",
			head, b.Units-b.CostlyUnits, formatCeiling(before), side)
	default:
		return head + "the ceiling is above the bound; if it is reached, the collectors last in the plan are the ones not run"
	}
}
```

`collect/collect.go`, `Check`: after the `Output   :` line is ended (`fmt.Println()`):

```go
	// Before the instance is touched, so that it is in every check that gets
	// past the corpus, including one that cannot connect or cannot price the
	// plan. Nothing is printed without a bound.
	if l := BoundLine(o.Config); l != "" {
		fmt.Println("Bound    : " + l)
	}
```

and in the Duration block, after the loop over `b.Lines()`:

```go
			if o.Config.MaxDuration > 0 {
				fmt.Printf("  %s\n", b.Against(o.Config.MaxDuration))
			}
```

`tui/state.go`, after `QueryStoreWindow string`:

```go
	// Bound is BoundLine of the resolved configuration, and MaxDuration the
	// bound itself for the comparison under the ceiling. Shown on screen 3,
	// never edited: .env remains the place where settings live.
	Bound       string
	MaxDuration time.Duration
```

`tui/run.go`, `initialState`: `Bound: collect.BoundLine(cfg), MaxDuration: cfg.MaxDuration,`.

`tui/render.go`, `renderOptions`: after the `b.Lines()` loop, inside the same `if`:

```go
		if s.MaxDuration > 0 {
			out = append(out, screen.Wrap(b.Against(s.MaxDuration), width, fieldPad)...)
		}
```

and after the Query Store window block:

```go
	if s.Bound != "" {
		out = append(out, screen.Wrap("Bound: "+s.Bound, width, pad)...)
		out = append(out, "")
	}
```

- [ ] Step 4: rerun: `top-level passes: 7` (the four new tests and three existing ones: `TestCheckPrintsTheCorpusBeforeItTouchesTheInstance`, `TestCheckStillWritesItsListingToStdout`, `TestPlannedDurationLines`). Full suite.

- [ ] Step 5: breaks.

| Change | Prediction |
| --- | --- |
| Print the `Bound` line inside the Duration block, after the ceiling, instead of after `Output   :` | red: `TestCheckPrintsTheBoundBeforeItConnects` (measured on the prototype) |
| `Against`: `b.Ceiling < limit` | red: "the ceiling at the bound" |
| `Against`: always "above" in the costly case | red: "costly on, the units before them under" |
| `BoundLine` writes `cfg.MaxDuration.String()` | red: `TestBoundLineNamesTheValueAndWhereItCameFrom` |
| Print the `Bound` line also without a bound (`fmt.Println("Bound    : " + BoundLine(o.Config))` with no `if`) | red: `TestCheckStillWritesItsListingToStdout` (`stdout mismatch under a non-nil Progress`), which compares the whole of what `check` writes to stdout. `TestCheckPrintsTheCorpusBeforeItTouchesTheInstance` stays green: it reads exactly `len(want)` bytes, which end at `Output   : ...`, and never sees a line after them. Measured on 5 October |
| `initialState` does not set `Bound` | predicted green: the render test builds the state itself. Report it; the wizard's own path is checked by reading. |

- [ ] Step 6: commit.

```bash
git add collect/duration.go collect/collect.go collect/duration_test.go collect/check_test.go tui/state.go tui/run.go tui/render.go tui/render_test.go
git commit -m "Show the bound in check and on the wizard's third screen

A bound forgotten in a .env, or given through the environment to a binary
that may ignore it, must be visible before the run. check prints it before
it connects, so that it is there even when the plan cannot be priced, and
compares it with the ceiling without forecasting where a run would stop,
since that forecast is hundreds of times too pessimistic."
```

### Task 23: documentation

Spec: "The option and its format" (README, changelog, dba-guide), "What the bound holds" (the promise), "Exit code and the previous run of the day" (the wizard's paragraph). No test; the review reads the text against the tree.

Files:
- Modify: `README.md`, `docs/dba-guide.md`, `CHANGELOG.md`, `collect/collect.go` (one comment in `Check`)

- [ ] Step 1: `README.md`.

In "Connection and output", after the `--keep` row:

```
| `--max-duration D` | bound the whole collection, counted from its start: once `D` has passed, no collector starts and the one running is stopped. A Go duration in whole seconds, at least one minute: `90m`, `2h`, `1h30m`. Overrides `MAX_DURATION`. See [Bounding the duration of a collection](#bounding-the-duration-of-a-collection) |
```

In "Collecting more than the default", in both rows (`--estimate-compression`, `--measure-page-density`), replace `and nothing bounds the run as a whole; ` with `and `--max-duration` bounds the run as a whole; ` (keeping `check` prints that ceiling before the run).

After the section "`--all` asks for the widest archive this tool can produce", a new section:

```markdown
### Bounding the duration of a collection

`--max-duration 2h`, or `MAX_DURATION=2h` in `.env`, bounds the whole
collection. The bound counts from the start of the run, connecting included,
the same origin as `duration_sec` in `_run.json`. Once it has passed, no
collector starts, and the statement running at that moment is cancelled. Every
collector not started is listed in `_run.json` with the reason, the run exits
`2`, and the archive is partial. The collection is bounded; nothing is judged.

What can still run after the bound: the server can take a few seconds to
confirm the cancellation of the statement in flight, and longer to undo what
that statement had written; the session's reset after a collector, the start of
the blocking watch and its last poll are not bounded and can each take up to
their own limits; the manifest and the archive are written after it. The bound
counts running time: a machine that sleeps, or a virtual machine paused, ends
that much later by the clock on the wall.

The collectors behind the two options off for cost run after all the others,
in every run, so that a bound reached late cuts them before it cuts the Query
Store. `check` prints the bound before it connects, with where it came from,
and compares it with the ceiling of the plan.

`MAX_DURATION` is new: 0.37.0 and older refuse a `.env` that sets it, which is
the safe outcome. They ignore it in the process environment without a word,
so set it in `.env` or with the flag, and look for the `Bound` line in
`check`.
```

In "Configuration", "What to run, and where it goes", after `OUTPUT_DIR`:

```
| `MAX_DURATION` | *(empty)* | bound on the whole collection, a Go duration in whole seconds of at least one minute (`90m`, `2h`); empty means none. See [Bounding the duration of a collection](#bounding-the-duration-of-a-collection) |
```

In "Exit codes", replace the wizard's paragraph with:

```markdown
The wizard is the exception for a stop: an operator who stops the collection
from the wizard has read the screen that calls the archive partial, and the
wizard exits `0`. A bound is the same decision taken in advance, and a run it
cut exits `0` in the wizard too, provided the run wrote an archive, collected
something, and no collector had failed on its own before the bound. A bound
that cut the run before anything was collected, or a bounded run in which a
collector had already failed, exits `2`.
```

In "The non-interactive path", after the bullet on `ctrl-c`, a bullet:

```markdown
- A collection cut by `--max-duration` exits `2`, its last line but one ends
  with `max duration reached` (then `cancelled` if it was also stopped), and a
  note on stderr says how many collectors were not started.
```

- [ ] Step 2: `docs/dba-guide.md`, "The recognised keys": add `MAX_DURATION` to the list, after `OUTPUT_DIR`.

- [ ] Step 3: `CHANGELOG.md`, under `## [Unreleased]`. In `### Added`:

```markdown
- `--max-duration D` and `MAX_DURATION` bound a whole collection. Once the bound has passed, counted from the start of the run, no collector starts, the statement running is cancelled, every collector not started is listed in `skipped_scripts` with the reason, `run.max_duration_reached` is set, `config.max_duration_sec` records the bound, and the run exits 2 with a partial archive. A Go duration in whole seconds of at least one minute. `check` prints the bound before it connects and compares it with the ceiling; the wizard shows it on its third screen. `.env.example` carries it commented, since 0.37.0 and older refuse a `.env` that sets it.
```

In `### Changed`:

```markdown
- The collectors behind `--estimate-compression` and `--measure-page-density` run after all the others, in every run, so that a bound or a stop reached late cuts them rather than the Query Store. The order of `results` in `_run.json` changes for a run with either option; a run with neither keeps its order.
- `collect.Observer.Finished` takes a `collect.Verdict` (cancelled, bound reached, failed on its own, units collected) in place of a boolean.
- The wizard's last screen counts as collected only the units that returned a result, from the run's own count; it counted every unit done, skips and errors included.
```

In the existing `### Added` entry about the duration ceiling, replace `Nothing bounds the collection as a whole yet; this only says before the run how long it can go.` with `--max-duration bounds the collection as a whole.`

- [ ] Step 4: `collect/collect.go`, in `Check`, the comment above the ceiling (`// The ceiling sits under the list it multiplies: ...`) still says the ceiling is announced "because nothing bounds a collection as a whole". Replace that clause, keeping the rest of the comment:

```go
	// The ceiling sits under the list it multiplies: a per-database
	// collector is paid once per line above. It is announced here because
	// only MAX_DURATION bounds a collection as a whole, and it is unset by
	// default: an operator about to start one in the evening has to know
	// whether the worst case is ten minutes or four hours before choosing the
	// flags, or the bound, not after.
```

(the two lines about an empty plan stay). `gofmt -l .` and `go build ./...` after the edit.

- [ ] Step 5: check the prose: `grep -nP '\x{2014}|\x{2013}' README.md docs/dba-guide.md CHANGELOG.md` finds nothing in the lines you added (the files may hold older ones; do not touch those). `git grep -niE "<client names you have been working with>"` per `CLAUDE.md`.

- [ ] Step 6: commit.

```bash
git add README.md docs/dba-guide.md CHANGELOG.md collect/collect.go
git commit -m "Document the bound on a collection

The README states the promise in the terms the spec settled: no collector
starts after the bound and the running statement is cancelled, with what
can still run past it. It says from which version the key exists and why
the environment is the wrong place for it, and how the wizard's exit code
treats a run the bound cut."
```

### Task 24: the live max-duration tests in CI

Spec: silent on CI. The owner ruled on 5 October 2026 that the `^TestLiveMaxDuration` tests run in the CI integration job, on both legs (SQL Server 2017 and 2022).

Files:
- Modify: `.github/workflows/ci.yml` (the `integration` job)

Read `.github/workflows/ci.yml` first: the `build` job runs `go test ./... -count=1` and `go test ./collect/ -count=2` with no database; the `integration` job starts SQL Server 2017 and 2022 containers, sets `SQL_SERVER`, `SQL_USER`, `SQL_PASSWORD` and `SQL_TRUST_SERVER_CERTIFICATE` at job level, never `SQL_AUDITOR_LIVE_*`, and runs `check` and `collect` through the binary, then asserts on the archive.

Without this step CI runs every test of this plan that needs no server (Tasks 1 to 10, the non-live parts of 11, 18 to 22: criteria 1, 2, 3, 4, 8, the manifest half of 9, 10's `summaryTail` and `TestSettleRun`, 11, 12, 13, 14, 15, 16b) and skips every live one (criteria 5a to 5d, 6a to 6d, 7, 16a, and the extra live tests of Tasks 11 and 14), so the call site `settleRun(exit, m.Run.Cancelled || m.Run.MaxDurationReached)`, the check before `lockRun`, the check before the watch and `cut` would have no test CI runs.

- [ ] Step 1: add, as the last step of the `integration` job (after the existing assertions, so that the archive checks of `collect` stay next to it):

```yaml
      # The bound's live tests, against the same container. They create
      # nothing: read-only instance collectors on master, and one missing
      # table that is never created. A separate step because some are
      # timing-sensitive (a bound of three seconds must outlast the
      # preamble). A live test skips when SQL_AUDITOR_LIVE_SERVER is unset,
      # and go test exits 0 on skips, so a SKIP fails the step.
      - name: max-duration live tests
        env:
          SQL_AUDITOR_LIVE_SERVER: localhost,1433
          SQL_AUDITOR_LIVE_USER: sa
          SQL_AUDITOR_LIVE_PASSWORD: ${{ env.SQL_PASSWORD }}
        run: |
          go test ./collect/ -run '^TestLiveMaxDuration' -count=1 -v | tee "$RUNNER_TEMP/maxdur.log"
          grep -q '^--- PASS' "$RUNNER_TEMP/maxdur.log"
          if grep -q '^--- SKIP' "$RUNNER_TEMP/maxdur.log"; then exit 1; fi
```

(GitHub runs a `run:` block under `bash -eo pipefail`, so a failing `go test` fails the step through the pipe; the skip test is an `if`, since `set -e` ignores a command negated with `!`.) `liveConfig` sets `TrustCert: true`, which the container's self-signed certificate needs; it reads only the three `SQL_AUDITOR_LIVE_*` variables.

- [ ] Step 2: check the YAML locally: `python3 -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/ci.yml"))'` exits 0, and the full suite is green. The workflow itself runs when the branch is pushed, which the owner does, not the implementer. When it has run, the controller reads both legs' logs: 13 `--- PASS` lines in the step, no `--- SKIP` (`TestLiveMaxDuration` matches the 13 tests of Tasks 11 to 17). Risk to weigh in that reading: `TestLiveMaxDurationAtTheCheckBeforeTheRunFolder` and its sibling need the preamble under three seconds; on a slow runner they fail saying the hook was never called, a flaky red rather than a silent green, and a red there is read before it is rerun.

- [ ] Step 3: commit.

```bash
git add .github/workflows/ci.yml
git commit -m "Run the bound's live tests in the integration job

The checks that make the bound work (the one before the run folder, the
one before the blocking watch, the cut of a running collector, and the
exit code given the bound) are exercised only by live tests, which CI
skipped for lack of an instance. The integration job already has one on
each leg, so the step points the live harness at it, and fails on a skip,
since go test reports a skipped live test as a pass."
```

## Closing the branch

Taken by the controller, not by an implementer.

- `gofmt -l .` empty, `go vet ./...`, `rtk proxy go test ./... -count=1`, `rtk proxy go test ./collect/ -count=2`, and the two cross-compiles of `ci.yml`.
- The live set in one invocation: `-run '^TestLiveMaxDuration'`, 13 top-level passes, plus `^(TestLiveAUnitLeavesNoTempTable|TestLiveRunAddressReachesTheRerunGuard)$`, 2.
- On the lab, `SELECT name FROM sys.databases WHERE name LIKE 'ZzMaxDur%'` returns nothing.
- Last, once, and never while a review panel or any other live run uses the lab: the existing live tests of the drop path and the watch, which Task 14 touches and no implementer runs, because they create and drop `ZzDroppedDuringRun` and `ZzWatchLive`. The owner allowed this one run on 5 October 2026.

  ```bash
  WT=/path/to/worktree && cd "$WT" && LOG=$(mktemp) && SQL_AUDITOR_LIVE_SERVER='localhost,11533' SQL_AUDITOR_LIVE_USER=sa SQL_AUDITOR_LIVE_PASSWORD="$(LAB_SA_PASSWORD_COMMAND)" rtk proxy go test ./collect/ -run '^(TestLiveADatabaseDroppedDuringTheRunIsSkippedNotFailed|TestLiveTheRerunGuardAfterADatabaseDroppedDuringTheRun|TestLiveDatabaseExistsAsksTheCatalog|TestLiveWatchRecordsAWait)$' -count=1 -v >"$LOG" 2>&1; echo "go test exit: $?"; echo "top-level passes: $(grep -c '^--- PASS' "$LOG")"; grep -E '^--- (FAIL|SKIP)|^(FAIL|ok) ' "$LOG"
  ```

  Expected `top-level passes: 4`. Then, whatever the result, check that the tests' cleanups dropped both databases: `SELECT name FROM sys.databases WHERE name IN (N'ZzDroppedDuringRun', N'ZzWatchLive') OR name LIKE N'ZzMaxDur%'` returns no row (through `sqlcmd` or the `sa` connection the controller uses for the lab). A row left means a cleanup failed: drop that database, and only that one, by its exact name, and say so in the ledger.
- `git grep -niE` for the client names of the session, per `CLAUDE.md`.
- Read, since no test sees them: the `USE` path reads its cause before `ucancel()`; `databaseExists(bound, ...)`; `recordWatchOutcome(..., cut, ...)` in the defer; `initialState` sets `Bound`; the two `Debugf` lines sit after their calls.
- Every rule of the spec's "least sure" list of the fifth review is either covered above or named in the next section.

## Spec points found ambiguous while planning

Each point says how this plan resolved it, or that it did not.

1. `runUnit`'s result order. The spec says `runUnit` "returns, beside its error, a second result, `cut bool`", which reads as `(error, bool)`, the prototype's form. Resolved as `(cut bool, err error)`, the Go convention of the error last; both are named results, which the deferred function needs. Nothing else depends on the order.
2. Criterion 15 tests "runUnit's deferred switch" without a server, which the switch inside a closure does not allow. Resolved by moving the switch into `recordWatchOutcome` (Task 18), a name the spec does not give. The call from the defer has no test (no way to make the watch fire during a bound stop on demand), as the spec's own "reasoned only" list concedes.
3. The `Observer.Finished` checklist omits two test files the signature change touches: `tui/run_test.go` builds `finishedEvent{cancelled: ...}` by hand, and `tui/loop_test.go` has a `runFinished` double implementing `exitStatus() int`, which the `coded` change breaks. Also `collect/unit_live_test.go` calls `runUnit` directly. Resolved by listing them in Tasks 11, 16 and 21. Not a contradiction, a gap in the checklist.
4. Criterion 11 asserts `skipLoses` "by name in its table test", and no table test of `skipLoses` or of `settingsLost` exists today. Resolved by creating both (Task 10); they pass before any code change, since they pin behaviour that must not move.
5. `BoundLine`'s return. The spec gives its signature, `BoundLine(cfg *Config) string`, and the line check prints, `Bound    : MAX_DURATION at ...`, without saying whether the function includes the `Bound    : ` label. Resolved: it returns the sentence after the label, `check` adds `Bound    : `, the wizard writes `Bound: ` (a wrapped screen would mangle the column padding).
6. Where the wizard's third screen puts the `Bound` line. The spec says "the same `Bound` line, and the comparison under the same ceiling, displayed and never edited, as the Query Store window is". Resolved: the comparison under the ceiling lines, the `Bound` line after the Query Store window line. Untested beyond a render test with a hand-built state.
7. `summaryTail`'s scope. The spec says the tokens are built by `summaryTail(m)`, with examples showing only the bound and the stop. Resolved: it also carries today's `, N partial` token, first, so that the whole tail of the line is one function.
8. The grouped entry of "Queries not run" recognises the bound's skips by equality with `maxDurationSkipReason` of the bound read back from `config.max_duration_sec`. The spec says the reason is "one constant string per run, built by one function, because MANIFEST.txt groups on it" without saying where `Human` gets the bound. Resolved with `recordedBound`; where the grouped entry sits in the list (at the first bound skip, as a dropped database's) is this plan's choice.
9. Criterion 6a's "the first unit returned within twelve seconds of the start": the prototype measured the whole `Run`. Resolved as the time from `Run`'s call to the first `UnitDone` the recorder sees, which is what the sentence says.
10. Criterion 10 says the call site of `settleRun` is held by criterion 16a, which this plan reaches in Task 15, one task after the call site changes in Task 14. Resolved by adding `TestLiveMaxDurationBetweenTwoUnits` to Task 14, a run cut only between units, which fails when `settleRun` is not given the bound. Task 14 also adds `TestLiveMaxDurationAfterTheLastUnitCutsNothing` for criterion 9's "absent when the bound passes after the last unit", which no spec criterion produces live.
11. Criterion 5b says to plant the previous run at the name `RunFolderFor` gives "with the server name the test reads on its own connection". The prototype instead ran an unbounded probe run to learn the name. This plan follows the spec (`Probe` and `RunServerName` on the test's own connection).
12. The owner's instruction limits live tests to databases named `ZzMaxDur…`. The existing live tests that exercise the code Task 14 changes on the drop path (`TestLiveADatabaseDroppedDuringTheRunIsSkippedNotFailed`, `TestLiveTheRerunGuardAfterADatabaseDroppedDuringTheRun`, `TestLiveDatabaseExistsAsksTheCatalog`) create and drop `ZzDroppedDuringRun`, and `TestLiveWatchRecordsAWait` creates `ZzWatchLive`. No implementer runs them. The owner ruled on 5 October 2026 that the controller may run them once, at the end of the branch and not beside a panel, followed by a check that both databases are gone: the last step of "Closing the branch".
13. The open questions of the spec that the owner has not ruled on stay open, and this plan implements the spec's current text for each: the one-minute floor; a stopped unit recorded as an error; the costly collectors moved last in every run, bounded or not; no `MANIFEST.txt` line for an operator's stop; one `skipped_scripts` entry per unit; no `check` warning against the measured preamble; the wizard exiting 0 for a cut run with something to send; the blocking watch's start left outside the bound. One of them asks whether `Verdict.Collected` and `CollectedUnits` should land on their own before the feature: this plan keeps them inside it (Tasks 11 and 20), and they can be taken out as a branch of their own without reordering anything else.
14. The spec's `stoppedOr` is reached from the check before `lockRun` with no step error, and its last line, `m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})`, would panic on a nil error if that path ever reached it with the bound not fired. It cannot, since the check calls `stoppedOr` only when the bound has fired and a context's cause never changes once set. Resolved by a comment at the call (Task 13); not guarded in code.
15. CI. The spec says nothing of CI, and the live criteria it measured are exactly the ones that hold the call sites (the check before `lockRun`, the check before the watch, `cut`, `settleRun` given the bound). The owner ruled for it on 5 October 2026: Task 24 adds the step to the integration job, on both legs.

## Review of the plan

A panel of five read this plan on 5 October 2026: a Claude reader on the neutral prompt, which applied Tasks 1 to 22 verbatim on a copy and ran them against the lab, agy on both prompts, and DeepSeek V4 Pro on both prompts in place of codex, whose two seats failed for capacity. What each finding became:

| Finding | Reader | Outcome |
| --- | --- | --- |
| A dial cut by the bound's deadline returns `i/o timeout` before the bound's timer has set the cause, so `stoppedOr` and the loop's guards read "not the bound" and the run exits 1 (criterion 5a red about 1 run in 300) | Claude | Taken, verified: a standalone program saw a nil cause after 200 dials of 200 made past a `WithDeadlineCause` deadline; the prototype's 5a test failed that way 1 time in 300 under `GOMAXPROCS=1`, and 0 in 600 with the fix. `settled` and `boundReached` through it (Task 6), `Connect` through it (Task 12), each with an offline test that fails on every run without the change (50 of 50, measured), using a context whose deadline has passed while its `Done` is held open. The spec says the rule under "The bound as a fact" |
| Task 19's second break stays green: an `[n/N]` line names the unit, never the reason | Claude | Taken, measured: the test also counts lines naming `80.workload/`; the break is red on the non-tty row |
| Task 22's fifth break names `TestCheckPrintsTheCorpusBeforeItTouchesTheInstance`, which reads only `len(want)` bytes; the spec's criterion 12 says the same | Claude | Taken: the break names `TestCheckStillWritesItsListingToStdout`, which joins the filter (7 passes); criterion 12 corrected |
| Task 12's step 2 predicts `cannot reach the instance` with no bound context yet; the run in fact completes | Claude | Taken: the step predicts exit 0, two units collected, and the hook never called |
| Task 21's break dropping `MaxDurationReached` names a row that stays green | Claude | Taken. On reading, step 2's prediction was wrong too (it named rows 2 to 5; today's body fails rows 1 and 4): corrected |
| Task 8's test uses `fmt`, which `manifest_test.go` does not import | Claude | Taken |
| Task 16 replaces the `runUnit` line with a block carrying the `pause` call Task 14 already added | Claude | Taken: the brief says to replace both lines |
| The comment in `Check` still says nothing bounds a collection | Claude | Taken: Task 23, step 4 |
| Criterion 14 asks `Render` of the final state driven through `loop`; the plan rendered hand-built states | Claude | Taken: rows 1 and 3 of `TestTheWizardsExitAfterABound` render the state the loop ended on; a break that leaves `ZipPath` empty is listed |
| "Review focus" item 4 calls the summary line pinned when only `summaryTail` is | Claude | Taken: the item says the call site is read, not tested |
| Task 6's first break, read as moving the parent test after the cause test, cannot fail: both guards return `err` unchanged | agy (neutral) | Taken in part: the parenthetical meant a cause test that returns the sentence before the parent is asked, which does fail (DeepSeek's reading). The row now gives the code |
| The live tests of the drop path and the watch, which Task 14 touches, never run | agy (directive), DeepSeek (neutral) | Taken by the owner's ruling: the controller runs them once at the end, then checks both databases are gone ("Closing the branch") |
| CI runs no live test of the bound | agy (directive) | Taken by the owner's ruling: Task 24, which also fails the step on a skip |
| `stoppedOr(2, nil)` would panic on `err.Error()` if the bound had not fired | agy (directive), DeepSeek (neutral) | Rejected. The only nil caller asks `boundReached` first, and a context's first cause never changes once set, so `stoppedOr` takes the bound's branch; with `settled` the two reads agree in the instant after the deadline too. A guard would have to invent a third outcome, an exit code with no error, that the spec does not define. The comment at the call (Task 13) stays |
| The summary line's call site in `Run` has no test | DeepSeek (neutral) | Known and listed (Task 9's predicted-green break); pinning it would change `Run`'s signature for one line. No change beyond the "Review focus" wording |
| The `USE` path has no live test | DeepSeek (neutral) | Known: the spec concedes it and the closing review reads the order. No change |
| `recordedBound` needs `strconv` in `manifest.go` | DeepSeek (neutral) | Already in Task 8's text. No change |
