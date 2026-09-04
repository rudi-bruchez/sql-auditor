# Replication collection, slice 1 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the pipeline the two things replication collection needs — a
selection pass that keeps a distribution database, and a way to aim a collector
at it and at nothing else — then write the three collectors that use them.

**Architecture:** Five Go changes, all additive, each independently testable
with the pure-function tests the package already has. Then four SQL collectors
built on one guard pattern that has been measured on a live instance. Nothing
here needs a permission the corpus does not already ask for.

**Tech Stack:** Go 1.27, `github.com/microsoft/go-mssqldb`, T-SQL against SQL
Server 2012 and later, PowerShell + ScriptDom for the grammar check.

**Spec:** `docs/replication-spec.md`, with `docs/verification-replication-guard.md`
as its measurement record. Read both before Task 6.

## Global Constraints

- **No client identifier anywhere.** Not in code, SQL, comments, test
  fixtures, file names or commit messages. See `CLAUDE.md`. Invented names
  only: `SQL01`, `SALESDB`, `192.0.2.0/24`, `example.com`.
- **SQL Server 2012 is the floor.** Every statement must parse under
  `TSql110Parser`; `pwsh -File tools/verify-corpus-grammar.ps1` is the check
  and must exit 0.
- **`go test ./...` passes before every commit.**
- **A header line never begins with an `@` word, even in prose.** The parser
  reads it as a directive. This is now a lint error, so it fails loudly.
- **Every collector statement carries** `OPTION (RECOMPILE, MAXDOP 1)`, and
  every file `SET NOCOUNT ON;` and
  `SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;`.
- **The corpus runs on one pinned connection** (`db.SetMaxOpenConns(1)`), so a
  `#temp` table outlives its batch and breaks the second database of a run. Use
  table variables. Never `CREATE TABLE #`.
- **The archive promise:** no permanent object is created, altered or deleted.
  Staging in table variables is inside that promise; anything touching a server
  object is not.

---

## File Structure

**Go, all in `collect/`:**

| File | Change | Responsibility |
| --- | --- | --- |
| `queryset.go` | modify | `@widened` directive, `KnownWidened`, `Script.Widened` |
| `runner.go` | modify | three flags on `DatabaseInfo`, `Selection.Widened`, the second pass in `SelectTargets`, the `CandidateDatabases` projection |
| `output.go` | modify | `DatabaseFolder.WidenedFor` and `RetentionReason`, and `MarkWidened` |
| `observer.go` | modify | `planUnits` pairs a widened folder only with a matching script |
| `collect.go` | modify | the `folders` call site, and `check` prints the retention reason |
| `verify.go` | modify | the second `folders` call site, so `check` and `collect` agree |
| `manifest.go` | modify | `writeTargets` renders the retention reason |

**SQL, in `queries/90.availability/`:**

| File | Change | Responsibility |
| --- | --- | --- |
| `040.replication.sql` | modify | header rewrite, plus the remote-distributor reading from `sys.servers` |
| `041.replication-publisher.sql` | create | publication-side catalog |
| `042.replication-distribution.sql` | create | distribution database: topology, agents, latency, errors |
| `043.replication-subscriber.sql` | create | subscription-side catalog |
| `044.replication-counters.sql` | create | replication performance counters, `VIEW SERVER STATE` |

**Documentation:**

| File | Change | Responsibility |
| --- | --- | --- |
| `docs/dba-guide.md` | modify | the widening, and what replication collection costs in rights (Task 14) |

Tasks 1–8 are the socle and are shippable on their own: they change no
collector output and are covered by unit tests plus one live check. Task 8 is the natural review gate before any SQL is written.

---

### Task 1: The `@widened` directive

**Files:**
- Modify: `collect/queryset.go`
- Test: `collect/queryset_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Script.Widened string`, empty for an ordinary collector and
  `"replication"` for one that may run against a widened database.
  `KnownWidened map[string]bool` is the closed vocabulary.

- [ ] **Step 1: Write the failing tests**

Add two cases to the table in `TestDiscoverLintErrors`:

```go
		{"unknown widened value", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\n-- @widened: everything\nSELECT 1;",
			"everything"},
```

And a new test beside `TestDiscoverParsesDirectives`:

```go
func TestDiscoverParsesWidened(t *testing.T) {
	fsys := fstest.MapFS{"queries/90.availability/041.a.sql": {Data: []byte(
		"-- @scope: database\n-- @resultsets: root:object\n-- @widened: replication\nSELECT 1;")}}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 script, got %d", len(got))
	}
	if got[0].Widened != "replication" {
		t.Errorf("Widened = %q, want %q", got[0].Widened, "replication")
	}
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./collect/ -run 'TestDiscoverLintErrors|TestDiscoverParsesWidened' -v`
Expected: `TestDiscoverParsesWidened` fails with `Widened = "", want "replication"`;
the lint case fails because no lint error is produced for `@widened`.

- [ ] **Step 3: Implement**

In `collect/queryset.go`, beside `KnownFlags`:

```go
// KnownWidened is the closed vocabulary of @widened values. A collector
// declares one when it is allowed to run against a database the selection
// widened back in for a specific purpose — see SelectTargets' second pass.
// Closed for the same reason KnownFlags is: a misspelt value would silently
// mean "ordinary collector", which is the failure this directive exists to
// prevent.
var KnownWidened = map[string]bool{"replication": true}
```

Add the field to `Script`:

```go
	// Widened names the widening purpose this script serves. Empty means the
	// script is never offered a database that only the second pass retained.
	Widened string
```

Add the case to `parseScript`'s switch, before `default`:

```go
		case "widened":
			if !KnownWidened[val] {
				setLint(fmt.Sprintf("@widened: unknown value %q; expected one of replication", val))
				break
			}
			s.Widened = val
```

The `default` case's message hand-lists the accepted directive names and must
gain `widened`, or the first person to misspell it is told to use a vocabulary
that does not contain the word they wanted:

```go
			setLint(fmt.Sprintf("unknown directive @%s; expected one of "+
				"scope, timeout, permissions, resultsets, min_version, "+
				"requires_flag, writer, widened, correlated — and note that a "+
				"header line must not begin with an @ word, even in prose", key))
```

Add a case to the lint table that pins it, so the list cannot drift again:

```go
		{"the unknown-directive message lists widened", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\n-- @widning: replication\nSELECT 1;", "widened"},
```

- [ ] **Step 4: Run the tests and watch them pass**

Run: `go test ./collect/ -v -run 'TestDiscoverLintErrors|TestDiscoverParsesWidened'`
Expected: PASS. Then `go test ./...` — all packages `ok`.

- [ ] **Step 5: Commit**

```bash
git add collect/queryset.go collect/queryset_test.go
git commit -m "Ajouter la directive @widened, avec son vocabulaire fermé"
```

---

### Task 2: The three replication flags on `DatabaseInfo`

**Files:**
- Modify: `collect/runner.go`
- Test: `collect/runner_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `DatabaseInfo.IsPublished`, `.IsSubscribed`, `.IsDistributor`, all
  `bool`. `SelectTargets` reads them in Task 3.

- [ ] **Step 1: Write the failing test**

```go
func TestDatabaseInfoCarriesReplicationFlags(t *testing.T) {
	d := DatabaseInfo{Name: "SALESDB", State: "ONLINE", HasAccess: true,
		IsPublished: true, IsDistributor: false, IsSubscribed: false}
	if !d.IsPublished || d.IsDistributor || d.IsSubscribed {
		t.Errorf("flags did not round-trip: %+v", d)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./collect/ -run TestDatabaseInfoCarriesReplicationFlags`
Expected: compile error — `unknown field IsPublished in struct literal`.

- [ ] **Step 3: Implement**

```go
type DatabaseInfo struct {
	Name, State string
	IsSnapshot  bool
	HasAccess   bool
	// The three replication roles, read from sys.databases in the same pass
	// that lists the candidates. They are flags and not proof of activity: a
	// database restored from a publisher keeps them set.
	IsPublished   bool
	IsSubscribed  bool
	IsDistributor bool
}
```

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./collect/ -run TestDatabaseInfoCarriesReplicationFlags`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add collect/runner.go collect/runner_test.go
git commit -m "Porter les trois drapeaux de réplication sur DatabaseInfo"
```

---

### Task 3: The second pass in `SelectTargets`

**Files:**
- Modify: `collect/runner.go`
- Test: `collect/runner_test.go`

**Interfaces:**
- Consumes: `DatabaseInfo`'s three flags from Task 2.
- Produces: `Selection.Widened map[string]WidenedFor` — database name to why the
  second pass kept it. `WidenedFor` has two fields and they must stay two:

```go
// WidenedFor is why a database the operator did not name is in the run.
type WidenedFor struct {
	// Purpose is a machine token from the same closed vocabulary as the
	// @widened directive. planUnits matches it against a script's own value.
	Purpose string
	// Reason is the sentence MANIFEST.txt shows a human. Nothing matches on it.
	Reason string
}
```

  A single field carrying both is the defect a review caught before any of this
  ran: `planUnits` compares to `"replication"`, the prose reason never equals
  it, and the widened database is then offered to no collector at all — the
  whole slice a silent no-op. Task 4 carries both onto `DatabaseFolder`; Task 5
  matches on `Purpose`; Task 6 renders `Reason`.

**The rule, restated from the spec so the implementer does not have to
reconstruct it:** a database carrying `IsDistributor` is retained even when
`DB_INCLUDE` does not match it, provided at least one database **retained after
filtering** carries `IsPublished`. `DB_EXCLUDE` still wins, and so do the state,
snapshot and access checks. A publisher that was excluded or is inaccessible
does not trigger retention — only one that survived into `Included`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSelectTargetsWidensToDistributor(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if !contains(sel.Included, "DISTDB") {
		t.Errorf("DISTDB should be retained; Included = %v", sel.Included)
	}
	if sel.Widened["DISTDB"].Purpose != "replication" {
		t.Errorf("DISTDB should carry the replication purpose; Widened = %v", sel.Widened)
	}
	if sel.Widened["DISTDB"].Reason == "" {
		t.Errorf("DISTDB should carry a retention reason; Widened = %v", sel.Widened)
	}
	// The superseded skip must be gone, or the manifest lists the database
	// twice with contradictory reasons.
	for _, s := range sel.Skipped {
		if s.Name == "DISTDB" {
			t.Errorf("DISTDB is both included and skipped: %q", s.Reason)
		}
	}
}

func TestSelectTargetsDoesNotWidenWithoutARetainedPublisher(t *testing.T) {
	cands := []DatabaseInfo{
		// Published, but excluded by the operator: it does not trigger.
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "OTHERDB", "SALESDB")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if contains(sel.Included, "DISTDB") {
		t.Errorf("no retained publisher, so DISTDB must not be widened in")
	}
}

func TestSelectTargetsExcludeBeatsWidening(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "DISTDB")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if contains(sel.Included, "DISTDB") {
		t.Errorf("DB_EXCLUDE must win over widening")
	}
}

func TestSelectTargetsDoesNotWidenAnInaccessibleDistributor(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: false, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if contains(sel.Included, "DISTDB") {
		t.Errorf("an inaccessible distributor must stay skipped")
	}
}

// A stale is_published flag on a restored database widens the run. The spec
// accepts this and says so; the test exists so the behaviour is deliberate
// rather than discovered.
func TestSelectTargetsWidensOnAStaleFlag(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "RESTOREDDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "RESTOREDDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if !contains(sel.Included, "DISTDB") {
		t.Errorf("a stale flag widens; the spec accepts this")
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./collect/ -run TestSelectTargets -v`
Expected: the four widening tests fail; `sel.Widened` does not compile
(`unknown field`). Fix the compile error by adding the field first if that is
easier to read, but do not implement the pass until the tests fail for the
right reason: `DISTDB should be retained`.

- [ ] **Step 3: Implement**

Add the field:

```go
type Selection struct {
	Included []string
	Skipped  []SkipReason
	// Widened maps a database name to the reason the second pass kept it
	// despite DB_INCLUDE. Empty for every ordinary run.
	Widened map[string]string
}
```

Append to `SelectTargets`, after the existing loop and before `return`:

```go
	// Second pass: keep the distribution database when a publisher survived
	// the first one.
	//
	// It fires only where it is needed. With no DB_INCLUDE every user database
	// is already included and this changes nothing. It is for the narrowed run
	// whose narrowed set still contains a publisher, where losing the
	// distributor that goes with it would be absurd.
	//
	// "Retained after filtering" is exact: a publisher the operator excluded,
	// or one the login cannot reach, does not trigger it.
	// "Retained" is the whole test: sel.Included already encodes every reason
	// the first pass had for keeping or dropping a database, so re-testing
	// DB_INCLUDE here would be a tautology.
	published := 0
	for _, d := range c {
		if d.IsPublished && containsString(sel.Included, d.Name) {
			published++
		}
	}
	if published == 0 {
		return sel, nil
	}
	for _, d := range c {
		if !d.IsDistributor || containsString(sel.Included, d.Name) {
			continue
		}
		// Everything except DB_INCLUDE still disqualifies it.
		if d.State != "ONLINE" || d.IsSnapshot || !d.HasAccess || matchAny(exc, d.Name) {
			continue
		}
		// The skip this supersedes has to go, or the manifest names the
		// database twice with two reasons.
		for i, s := range sel.Skipped {
			if s.Name == d.Name && s.Reason == "not matched by DB_INCLUDE" {
				sel.Skipped = append(sel.Skipped[:i], sel.Skipped[i+1:]...)
				break
			}
		}
		sel.Included = append(sel.Included, d.Name)
		if sel.Widened == nil {
			sel.Widened = map[string]string{}
		}
		sel.Widened[d.Name] = fmt.Sprintf(
			"local distributor for %d published database(s) in this selection", published)
	}
	return sel, nil
```

And the helper, beside `matchAny`:

```go
func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run them and watch them pass**

Run: `go test ./collect/ -run TestSelectTargets -v`
Expected: all PASS. Then `go test ./...`.

- [ ] **Step 5: Commit**

```bash
git add collect/runner.go collect/runner_test.go
git commit -m "Réintégrer la base de distribution quand un publisher survit au filtrage"
```

---

### Task 4: `DatabaseFolder.WidenedFor`

**Files:**
- Modify: `collect/output.go`
- Test: `collect/output_test.go`

**Interfaces:**
- Consumes: `Selection.Widened` from Task 3.
- Produces: `DatabaseFolder.WidenedFor string` (the purpose, matched by Task 5),
  `DatabaseFolder.RetentionReason string` (the prose, rendered by Task 6), and
  `MarkWidened(folders []DatabaseFolder, widened map[string]WidenedFor) []DatabaseFolder`.

**Two fields, not one.** The purpose is compared; the reason is read. Carrying
them in one field makes the comparison in Task 5 always false and the widened
database reaches no collector — with every task's own test still green, because
each fixture invents its own idea of what the field holds. The test below
therefore asserts both fields at once, which is what makes it able to fail.

- [ ] **Step 1: Write the failing test**

```go
func TestMarkWidenedTagsOnlyTheWidenedFolders(t *testing.T) {
	folders := ResolveDatabaseFolders([]string{"SALESDB", "DISTDB"})
	got := MarkWidened(folders, map[string]WidenedFor{
		"DISTDB": {Purpose: "replication", Reason: "local distributor for 1 published database(s) in this selection"},
	})
	for _, f := range got {
		switch f.Name {
		case "DISTDB":
			if f.WidenedFor != "replication" {
				t.Errorf("DISTDB WidenedFor = %q, want the purpose planUnits matches on", f.WidenedFor)
			}
			if !strings.Contains(f.RetentionReason, "local distributor") {
				t.Errorf("DISTDB RetentionReason = %q, want the human sentence", f.RetentionReason)
			}
		case "SALESDB":
			if f.WidenedFor != "" || f.RetentionReason != "" {
				t.Errorf("SALESDB must not be marked, got %q / %q", f.WidenedFor, f.RetentionReason)
			}
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./collect/ -run TestMarkWidened`
Expected: compile error — `undefined: MarkWidened`.

- [ ] **Step 3: Implement**

```go
type DatabaseFolder struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
	// WidenedFor is empty for an ordinarily selected database. When the
	// selection's second pass brought a database back, it carries the purpose
	// — "replication" — and planUnits offers the folder only to collectors
	// declaring the same @widened value. It is a token, never a sentence:
	// putting the human explanation here instead makes that comparison false
	// for every collector and the database is then read by none of them.
	WidenedFor string `json:"widened_for,omitempty"`
	// RetentionReason is that explanation, for MANIFEST.txt and for check.
	// Nothing matches on it.
	RetentionReason string `json:"retention_reason,omitempty"`
}

// MarkWidened stamps the retention purpose and reason onto the folders the
// second pass kept. It is separate from ResolveDatabaseFolders because folder
// naming is about collisions on disk and this is about who may read the
// database; a single function doing both would have two reasons to change.
func MarkWidened(folders []DatabaseFolder, widened map[string]WidenedFor) []DatabaseFolder {
	if len(widened) == 0 {
		return folders
	}
	for i := range folders {
		if w, ok := widened[folders[i].Name]; ok {
			folders[i].WidenedFor = w.Purpose
			folders[i].RetentionReason = w.Reason
		}
	}
	return folders
}
```

`output_test.go` does not import `"strings"` today; the assertion above needs it.

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./collect/ -run TestMarkWidened` then `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add collect/output.go collect/output_test.go
git commit -m "Marquer les dossiers d'une base réintégrée avec le motif de rétention"
```

---

### Task 5: `planUnits` pairs a widened folder only with a matching collector

**Files:**
- Modify: `collect/observer.go`
- Modify: `collect/collect.go` — the call site that builds `folders`
- Test: `collect/observer_test.go`

**Interfaces:**
- Consumes: `Script.Widened` (Task 1), `DatabaseFolder.WidenedFor` (Task 4).
- Produces: nothing new; changes which units exist.

**Why this is not a skip.** Roughly thirty database-scoped collectors exist.
Recording a `SkippedScript` for each of them against the widened database would
put thirty "Queries not run" lines into every widened run. The folder is simply
never offered to them, and the manifest's retention reason on the database is
where a reader learns what happened.

- [ ] **Step 1: Write the failing test**

```go
func TestPlanUnitsKeepsAWidenedFolderForItsOwnCollectors(t *testing.T) {
	repl := Script{Path: "90.availability/042.a.sql", Scope: ScopeDatabase,
		Widened: "replication", Results: []ResultSpec{{"root", ShapeObject}}}
	ordinary := Script{Path: "70.schema/010.objects.sql", Scope: ScopeDatabase,
		Results: []ResultSpec{{"root", ShapeObject}}}
	folders := []DatabaseFolder{
		{Name: "SALESDB", Folder: "SALESDB"},
		{Name: "DISTDB", Folder: "DISTDB", WidenedFor: "replication"},
	}
	plan := []plannedScript{{Script: repl}, {Script: ordinary}}

	units, skipped, errs := planUnits(plan, folders, &Config{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	var replTargets, ordinaryTargets []string
	for _, u := range units {
		if u.Script.Path == repl.Path {
			replTargets = append(replTargets, u.Target.Name)
		} else {
			ordinaryTargets = append(ordinaryTargets, u.Target.Name)
		}
	}
	if len(replTargets) != 2 {
		t.Errorf("the replication collector wants both databases, got %v", replTargets)
	}
	if len(ordinaryTargets) != 1 || ordinaryTargets[0] != "SALESDB" {
		t.Errorf("the ordinary collector must not see DISTDB, got %v", ordinaryTargets)
	}
	// Not a skip: nothing is recorded for the pairing that did not happen.
	for _, s := range skipped {
		if strings.Contains(s.Reason, "DISTDB") || s.Target == "DISTDB" {
			t.Errorf("a widened folder must not produce a skip entry: %+v", s)
		}
	}
}
```

And a second test that builds the folders the way the program does, rather than
by hand. The test above passes even when `MarkWidened` writes the wrong value
into `WidenedFor`, because its fixture writes the right one; only running the
three functions in sequence can show that they agree:

```go
func TestWidenedDatabaseSurvivesSelectionIntoUnits(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	sel, err := SelectTargets(cands, "SALESDB", "")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	folders := MarkWidened(ResolveDatabaseFolders(sel.Included), sel.Widened)

	repl := Script{Path: "90.availability/042.a.sql", Scope: ScopeDatabase,
		Widened: "replication", Results: []ResultSpec{{"root", ShapeObject}}}
	units, _, errs := planUnits([]plannedScript{{Script: repl}}, folders, &Config{})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %+v", errs)
	}
	var got []string
	for _, u := range units {
		got = append(got, u.Target.Name)
	}
	if len(got) != 2 {
		t.Fatalf("the collector the widening was for must see both databases, got %v", got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./collect/ -run TestPlanUnitsKeepsAWidenedFolder -v`
Expected: `the ordinary collector must not see DISTDB, got [SALESDB DISTDB]`.

- [ ] **Step 3: Implement**

In `planUnits`, inside the `if s.Scope == ScopeDatabase` branch, after
`targets, narrowed = queryStoreUnits(cfg, s, folders)`:

```go
			// A folder the selection widened back in is offered only to the
			// collectors that widening was for. It is not a skip: recording
			// one per ordinary collector would write thirty "Queries not run"
			// lines into every widened run, describing a pairing nobody asked
			// for.
			// A new slice, never targets[:0]. queryStoreUnits returns the
			// caller's slice unchanged for every script without a @writer, so
			// targets aliases folders, and filtering in place would rewrite
			// the shared list for every later script. Demonstrated: after one
			// ordinary script, folders becomes [SALESDB, SALESDB] — one
			// database collected twice and the other gone.
			kept := make([]DatabaseFolder, 0, len(targets))
			for _, t := range targets {
				if t.WidenedFor == "" || t.WidenedFor == s.Widened {
					kept = append(kept, t)
				}
			}
			targets = kept
```

`ResolveDatabaseFolders` has **two** call sites and both need the marking, or
`check` and `collect` disagree about the same run:

`collect/collect.go:1278`:

```go
	folders := MarkWidened(ResolveDatabaseFolders(sel.Included), sel.Widened)
```

`collect/verify.go:155` — this is what `check` prints under "Databases that
would be collected", and it is the one place a DBA looks *before* authorising
the run. Without it, `check` shows the distribution database with nothing
saying the operator did not ask for it:

```go
		v.Folders = MarkWidened(ResolveDatabaseFolders(sel.Included), sel.Widened)
```

Marking the folder is not enough on its own: the code that renders `check`,
`collect/collect.go:667-669`, prints only the name and the folder, so the DBA
would see the distribution database appear with nothing saying why. Give it the
reason:

```go
		for _, f := range v.Folders {
			if f.RetentionReason != "" {
				fmt.Printf("  - %s -> %s/ (%s)\n", f.Name, f.Folder, f.RetentionReason)
				continue
			}
			fmt.Printf("  - %s -> %s/\n", f.Name, f.Folder)
		}
```

The test file needs `"strings"` in its imports for the skip assertion above;
`observer_test.go` does not import it today.

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./collect/ -run TestPlanUnits -v` then `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add collect/observer.go collect/collect.go collect/observer_test.go
git commit -m "N'offrir une base réintégrée qu'aux collecteurs pour qui elle l'a été"
```

---

### Task 6: The manifest says why a database it was not asked for is there

**Files:**
- Modify: `collect/manifest.go`
- Test: `collect/manifest_test.go`

**Interfaces:**
- Consumes: `DatabaseFolder.RetentionReason` — the prose field, not the purpose
  token beside it.
- Produces: nothing.

- [ ] **Step 1: Write the failing test**

```go
func TestManifestExplainsAWidenedDatabase(t *testing.T) {
	m := NewManifest("SQL01", "11.0.7001.0", "")
	// Name, version and commit are constructor arguments, not fields.
	m.Targets.Databases = []DatabaseFolder{
		{Name: "SALESDB", Folder: "SALESDB"},
		{Name: "DISTDB", Folder: "DISTDB", WidenedFor: "replication",
			RetentionReason: "local distributor for 1 published database(s) in this selection"},
	}
	h := flatten(m.Human())
	if !strings.Contains(h, "local distributor for 1 published database(s)") {
		t.Errorf("MANIFEST.txt must say why DISTDB is here:\n%s", m.Human())
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./collect/ -run TestManifestExplainsAWidenedDatabase`
Expected: FAIL — the reason is not rendered.

- [ ] **Step 3: Implement**

In `writeTargets`, in the loop over `m.Targets.Databases`, append the reason
when it is set:

```go
			if d.RetentionReason != "" {
				fmt.Fprintf(b, "      kept because: %s\n", d.RetentionReason)
			}
```

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./collect/ -run TestManifest -v` then `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add collect/manifest.go collect/manifest_test.go
git commit -m "Dire dans le manifeste pourquoi une base non demandée a été collectée"
```

---

### Task 7: `CandidateDatabases` reads the three flags

**Files:**
- Modify: `collect/runner.go:256-278`

**Interfaces:**
- Consumes: `DatabaseInfo`'s flags (Task 2).
- Produces: populated flags for `SelectTargets`.

This is the one task with no unit test: the function issues SQL and the package
has no fake for a `*sql.Conn`. It is verified against the container instead,
which is what the repository does elsewhere for connection-bound code.

- [ ] **Step 1: Change the projection**

```go
	rows, err := c.QueryContext(ctx, `
        SELECT d.name, d.state_desc,
               CASE WHEN d.source_database_id IS NULL THEN 0 ELSE 1 END,
               CASE WHEN HAS_DBACCESS(d.name) = 1 THEN 1 ELSE 0 END,
               CONVERT(int, d.is_published),
               CONVERT(int, d.is_subscribed),
               CONVERT(int, d.is_distributor)
        FROM sys.databases AS d
        WHERE d.database_id > 4
        ORDER BY d.name`)
```

And the scan:

```go
	for rows.Next() {
		var d DatabaseInfo
		var snap, acc, pub, sub, dist int
		if err := rows.Scan(&d.Name, &d.State, &snap, &acc, &pub, &sub, &dist); err != nil {
			return nil, err
		}
		d.IsSnapshot, d.HasAccess = snap == 1, acc == 1
		d.IsPublished, d.IsSubscribed, d.IsDistributor = pub == 1, sub == 1, dist == 1
		out = append(out, d)
	}
```

- [ ] **Step 2: Verify the SQL against a live instance**

Start the container if it is not running:

```bash
podman run -d --name sqlauditor-review -e ACCEPT_EULA=Y \
  -e MSSQL_SA_PASSWORD='Str0ng!Passw0rd' -p 11433:1433 \
  mcr.microsoft.com/mssql/server:2022-latest
```

Then run the projection verbatim. The container's port is published on IPv6
only; `localhost` and `127.0.0.1` do not answer.

```bash
sqlcmd -S "[::1],11433" -U sa -P 'Str0ng!Passw0rd' -C -Q "
SELECT d.name, d.state_desc,
       CASE WHEN d.source_database_id IS NULL THEN 0 ELSE 1 END,
       CASE WHEN HAS_DBACCESS(d.name) = 1 THEN 1 ELSE 0 END,
       CONVERT(int, d.is_published),
       CONVERT(int, d.is_subscribed),
       CONVERT(int, d.is_distributor)
FROM sys.databases AS d WHERE d.database_id > 4 ORDER BY d.name"
```

Expected: no error, seven columns. On a bare instance there may be zero rows,
which is fine — the point is that the three columns exist and parse.

- [ ] **Step 3: Run the whole suite**

Run: `go test ./...`
Expected: all `ok`.

- [ ] **Step 4: Commit**

```bash
git add collect/runner.go
git commit -m "Projeter les trois drapeaux de réplication dans la liste des candidates"
```

---

## Review gate — after Task 8, not here

Task 8 belongs to the socle and comes next; take the gate after it, when the
whole Go side is done and no SQL exists yet.

**Stop then.** Tasks 1–8 are the socle: they change no archive content and are
covered by unit tests plus one live check. Before writing any SQL, hand the
diff to a fresh reviewer. The questions worth asking are whether the second
pass can retain a database the first pass skipped for a reason other than
`DB_INCLUDE`, and whether `planUnits`' filter can drop a folder for a collector
that should have had it.

---

### Task 8: Teach the collision test that a hint inside a string is not a statement

**Files:**
- Modify: `collect/queryset.go`
- Modify: `queries_test.go:120`
- Test: `collect/queryset_test.go`, `queries_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `collect.BlankSQLStrings(sql string) string`, which returns `sql`
  with the *contents* of single-quoted literals replaced by spaces, leaving
  every other byte and the total length untouched.

**Why this comes before any SQL is written.** `TestEmbeddedCorpusHasNoTopLevelKeyCollision`
counts emitting statements by splitting the raw file on the literal string
`OPTION (RECOMPILE, MAXDOP 1)`. The guard pattern puts that hint inside the
`sp_executesql` string as well as on the outer statement, so every collector in
this slice reports two to nine more emitting statements than it has result sets
and the test fails. Measured on all four: 7 for 4, 16 for 7, 5 for 3, 3 for 2.

The collector files are not wrong. The test's model of "an emitting statement"
predates dynamic SQL, and the guard pattern is now the house pattern for any
collector that reads an object which may not exist. Left alone, this test
blocks the pattern everywhere and the obvious workaround — dropping the inner
hints — would run a distributor's history aggregate without `MAXDOP 1` on the
one instance where that matters.

Note that `contractLint` in `queryset.go` deliberately *does* see hints inside
literals, and should keep doing so: it asks "does every statement carry the
hint", and a dynamic statement carrying it is a correct answer. Only the
counting test needs the narrower view.

- [ ] **Step 1: Write the failing test for the helper**

In `collect/queryset_test.go`:

```go
func TestBlankSQLStringsKeepsCodeAndLength(t *testing.T) {
	in := `SELECT 'a''b' AS x, 1 OPTION (RECOMPILE, MAXDOP 1);
EXEC sp_executesql N'SELECT 1 OPTION (RECOMPILE, MAXDOP 1)';`
	got := BlankSQLStrings(in)
	if len(got) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(got))
	}
	if strings.Count(got, "OPTION (RECOMPILE, MAXDOP 1)") != 1 {
		t.Errorf("the hint inside the literal should be blanked; got:\n%s", got)
	}
	if !strings.Contains(got, "EXEC sp_executesql") {
		t.Errorf("code outside literals must survive; got:\n%s", got)
	}
	if strings.Contains(got, "a''b") {
		t.Errorf("literal contents must be blanked; got:\n%s", got)
	}
}

// The one that matters. Half the corpus is commented in English and in French,
// and an apostrophe in a comment is not the start of a string literal. A
// version that does not know this flips into "inside a literal" on the first
// "qu'est-ce" or "client's" and blanks the real code that follows — measured
// on the corpus as 30 files losing hints they carry.
func TestBlankSQLStringsIgnoresApostrophesInComments(t *testing.T) {
	in := `-- On regarde ce qu'est un plan ici.
SELECT 1 OPTION (RECOMPILE, MAXDOP 1);
/* And the client's own rules apply. */
SELECT 2 OPTION (RECOMPILE, MAXDOP 1);`
	got := BlankSQLStrings(in)
	if n := strings.Count(got, "OPTION (RECOMPILE, MAXDOP 1)"); n != 2 {
		t.Errorf("both hints are in code and must survive, got %d:\n%s", n, got)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./collect/ -run TestBlankSQLStrings`
Expected: compile error — `undefined: BlankSQLStrings`.

- [ ] **Step 3: Implement**

In `collect/queryset.go`, beside `StripSQLComments`:

```go
// BlankSQLStrings replaces the contents of single-quoted literals with spaces,
// keeping every other byte and the total length. It exists so that a test can
// count statements in code without seeing the SQL a collector passes to
// sp_executesql: the guard pattern repeats the query hint inside that string,
// and a naive split on the hint text counts the dynamic statement as a second
// emitting one.
//
// It is deliberately not StripSQLComments' job. That function keeps literals
// because contractLint must see a hint wherever it is written, including
// inside dynamic SQL. The two callers want opposite things from the same text.
//
// It runs the same four-state machine as StripSQLComments, and it has to: an
// apostrophe in a comment does not open a string literal. Half this corpus is
// commented in French, so a quote-counting version reads "qu'est-ce" as the
// start of a literal and blanks the code after it. Comments themselves are
// left alone, which keeps the counting test's behaviour on them exactly as it
// was before this function existed.
func BlankSQLStrings(sql string) string {
	b := []byte(sql)
	blank := func(i int) {
		if b[i] != '\n' && b[i] != '\r' {
			b[i] = ' '
		}
	}
	const (
		code = iota
		lineComment
		blockComment
		literal
	)
	state := code
	blockDepth := 0
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch state {
		case code:
			switch {
			case c == '-' && i+1 < len(b) && b[i+1] == '-':
				state = lineComment
				i++
			case c == '/' && i+1 < len(b) && b[i+1] == '*':
				state = blockComment
				blockDepth = 1
				i++
			case c == '\'':
				state = literal
			}
		case lineComment:
			if c == '\n' {
				state = code
			}
		case blockComment:
			switch {
			case c == '/' && i+1 < len(b) && b[i+1] == '*':
				blockDepth++
				i++
			case c == '*' && i+1 < len(b) && b[i+1] == '/':
				blockDepth--
				i++
				if blockDepth == 0 {
					state = code
				}
			}
		case literal:
			if c == '\'' {
				// '' is an escaped quote and still contents, not the close.
				if i+1 < len(b) && b[i+1] == '\'' {
					blank(i)
					blank(i + 1)
					i++
					continue
				}
				state = code
				continue
			}
			blank(i)
		}
	}
	return string(b)
}
```

- [ ] **Step 4: Run it and watch it pass**

Run: `go test ./collect/ -run TestBlankSQLStrings -v`
Expected: PASS.

- [ ] **Step 5: Use it in the collision test**

In `queries_test.go`, change the split to work on the blanked text while the
alias scan keeps working on the real one — the aliases live in code, so
blanking changes nothing for them, and using one string for both keeps the
positional match honest:

```go
		chunks := strings.Split(collect.BlankSQLStrings(s.SQL), "OPTION (RECOMPILE, MAXDOP 1)")
```

- [ ] **Step 6: Prove the change does not weaken the test**

Run: `go test . -run TestEmbeddedCorpusHasNoTopLevelKeyCollision -v`
Expected: PASS on the current 58-file corpus — no file loses coverage, because
no shipped collector uses dynamic SQL with a hint inside it yet.

Then measure it directly, because a PASS above only says the counts still
match, and this function's failure mode is silent erasure. Add this to
`queries_test.go` and keep it — it is what stops the next reader from
simplifying the state machine back into a quote counter:

```go
func TestBlankSQLStringsErasesNoHintInTheCurrentCorpus(t *testing.T) {
	const hint = "OPTION (RECOMPILE, MAXDOP 1)"
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range scripts {
		before := strings.Count(s.SQL, hint)
		after := strings.Count(collect.BlankSQLStrings(s.SQL), hint)
		if before != after {
			t.Errorf("%s: %d hints before blanking, %d after", s.Path, before, after)
		}
	}
}
```

Expected: PASS, all 58 files. A quote-counting version fails it on 30 of them —
`013.memory-model.sql` 1→0, `050.tempdb.sql` 11→5, `040.error-log.sql` 6→3, and
so on down the corpus.

Then confirm the test still catches what it was written for, by temporarily
adding a root column `[waits.n]` to a file declaring a `waits` array, running
the test, seeing it fail with "claims the top-level key", and reverting.

- [ ] **Step 7: Commit**

```bash
git add collect/queryset.go collect/queryset_test.go queries_test.go
git commit -m "Ne plus compter comme instruction un hint écrit dans une chaîne dynamique"
```

---

### Task 9: `041.replication-publisher.sql`

**Files:**
- Create: `queries/90.availability/041.replication-publisher.sql`

**Interfaces:**
- Consumes: `@widened: replication` (Task 1).
- Produces: an archive document with `applies`, `collected`, `error_number`,
  `error_message` in the root object and three arrays.

**The guard pattern is not negotiable and not improvisable.** It was measured;
`docs/verification-replication-guard.md` holds the outputs. There is no
`OBJECT_ID` test — it returns NULL for an object the login may not see, which
turns a refusal into an apparent absence.

- [ ] **Step 1: Write the file**

```sql
-- @scope:       database
-- @resultsets:  root:object, publications:array, articles:array, subscriptions:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @widened:     replication
--
-- What a publisher publishes, and on what terms.
--
-- THE READ IS DEFERRED THROUGH sp_executesql, AND THAT IS THE WHOLE
-- MECHANISM. dbo.syspublications exists only in a published database. A direct
-- SELECT against it raises Msg 208 when it is absent, at compile time, where a
-- TRY/CATCH at the same level cannot catch it and the batch dies with its
-- result sets half emitted. Inside sp_executesql the same error is a runtime
-- one and is caught. Measured, both ways, in docs/verification-replication-guard.md.
--
-- THERE IS NO OBJECT_ID TEST, AND THERE USED TO BE. OBJECT_ID answers "may I
-- see this" rather than "does this exist": it returns NULL for an object the
-- login has no rights on. Guarding with it turned error 229 into an empty
-- result set, so a publisher with publications was recorded as one with none.
--
-- IMMEDIATE_SYNC AND ALLOW_ANONYMOUS ARE WHY THIS FILE EXISTS. Together they
-- make the distribution database keep every command for the full retention
-- period whether or not a subscriber has taken it, which is the ordinary
-- explanation for a publisher log that will not truncate. Read beside
-- 20.databases/024.log-stats.sql, they turn log_reuse_wait = REPLICATION from
-- a symptom into a cause.
--
-- THE HOMONYM TRAP. syspublications, sysarticles and syssubscriptions exist as
-- tables here and as views with different columns in a distribution database.
-- This file's projections are written for the publication database; a column
-- list that happens to compile in the other one is a silent wrong answer.
--
-- NO PASSWORD COLUMN IS PROJECTED. syspublications carries ftp_password.
-- Projections stay explicit for that reason and must never drift to SELECT *.
--
-- SQL Server 2012 is the floor. Nothing here is newer.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @applies bit = 0, @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

SELECT @applies = CONVERT(bit, d.is_published)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @pub TABLE (
    [name] sysname, [status] tinyint, [repl_freq] tinyint, [sync_method] tinyint,
    [retention] int, [allow_push] bit, [allow_pull] bit, [allow_anonymous] bit,
    [immediate_sync] bit, [independent_agent] bit, [allow_sync_tran] bit,
    [allow_queued_tran] bit, [pubid] int);

DECLARE @art TABLE (
    [artid] int, [name] sysname, [dest_table] sysname, [objid] int, [pubid] int);

DECLARE @sub TABLE (
    [artid] int, [srvid] smallint, [srvname] sysname NULL, [dest_db] sysname,
    [status] tinyint, [sync_type] tinyint, [subscription_type] int);

IF @applies = 1
BEGIN
    BEGIN TRY
        INSERT INTO @pub
        EXEC sys.sp_executesql N'
            SELECT p.name, p.status, p.repl_freq, p.sync_method, p.retention,
                   p.allow_push, p.allow_pull, p.allow_anonymous,
                   p.immediate_sync, p.independent_agent, p.allow_sync_tran,
                   p.allow_queued_tran, p.pubid
            FROM dbo.syspublications AS p
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @art
        EXEC sys.sp_executesql N'
            SELECT a.artid, a.name, a.dest_table, a.objid, a.pubid
            FROM dbo.sysarticles AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @sub
        EXEC sys.sp_executesql N'
            SELECT s.artid, s.srvid, s.srvname, s.dest_db, s.status,
                   s.sync_type, s.subscription_type
            FROM dbo.syssubscriptions AS s
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       CONVERT(int, @collected)                     AS [collected],
       @err                                         AS [error_number],
       NULLIF(@msg, N'')                            AS [error_message],
       (SELECT COUNT(*) FROM @pub)                  AS [counts.publications],
       (SELECT COUNT(*) FROM @art)                  AS [counts.articles],
       (SELECT COUNT(*) FROM @sub)                  AS [counts.subscriptions]
OPTION (RECOMPILE, MAXDOP 1);

SELECT p.[name], p.[pubid], p.[status], p.[repl_freq], p.[sync_method],
       p.[retention], CONVERT(int, p.[allow_push])         AS [allow_push],
       CONVERT(int, p.[allow_pull])                        AS [allow_pull],
       CONVERT(int, p.[allow_anonymous])                   AS [allow_anonymous],
       CONVERT(int, p.[immediate_sync])                    AS [immediate_sync],
       CONVERT(int, p.[independent_agent])                 AS [independent_agent],
       CONVERT(int, p.[allow_sync_tran])                   AS [allow_sync_tran],
       CONVERT(int, p.[allow_queued_tran])                 AS [allow_queued_tran]
FROM @pub AS p ORDER BY p.[name]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[artid], a.[name], a.[dest_table], a.[objid], a.[pubid]
FROM @art AS a ORDER BY a.[pubid], a.[name]
OPTION (RECOMPILE, MAXDOP 1);

SELECT s.[artid], s.[srvid], s.[srvname], s.[dest_db], s.[status],
       s.[sync_type], s.[subscription_type]
FROM @sub AS s ORDER BY s.[artid]
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] **Step 2: Run the corpus lint**

This task adds a file, so `queries_test.go:18` — which asserts
`len(scripts) != 58` — must be raised by one in the same commit. The slice adds
four files in all, ending at 62.

Run: `go test . -run TestEmbeddedCorpus -v`
Expected: PASS. A failure names the directive or contract rule broken. If it
reports "N emitting statements for M result sets", Task 8 was skipped.

- [ ] **Step 3: Check the grammar and the result-set count**

Run: `pwsh -File tools/verify-corpus-grammar.ps1`
Expected: the file appears as `TSql110 (SQL Server 2012)  resultsets 4/4  ok`,
and the run ends `All N files parse...` with exit 0.

- [ ] **Step 4: Run it against the container**

```bash
sqlcmd -S "[::1],11433" -U sa -P 'Str0ng!Passw0rd' -C -d master \
  -i queries/90.availability/041.replication-publisher.sql
```

Expected: four result sets. `applies` is 0 and `collected` is 1 on a database
that publishes nothing; the three arrays come back empty with their headers.
No `Msg 208`.

- [ ] **Step 5: Commit**

```bash
git add queries/90.availability/041.replication-publisher.sql
git commit -m "Collecter le catalogue de publication, gardé par sp_executesql"
```

---

### Task 10: `043.replication-subscriber.sql`

**Files:**
- Create: `queries/90.availability/043.replication-subscriber.sql`

**Interfaces:**
- Consumes: the same guard pattern.
- Produces: root object plus two arrays.

Written before 042 because it is the smaller of the two and exercises the same
pattern once more before the large one.

- [ ] **Step 1: Write the file**

```sql
-- @scope:       database
-- @resultsets:  root:object, subscriptions:array, agents:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @widened:     replication
--
-- What this database subscribes to, and when it last heard from the
-- Distribution Agent.
--
-- ON A PULL SUBSCRIPTION THIS IS THE ONLY PLACE THE TOPOLOGY IS VISIBLE AT
-- ALL. The agent runs on the subscriber and its history lives on the
-- distributor, which may be a server this audit never touches. [Time] going
-- stale is then the whole signal.
--
-- The guard is the one described in 041 and measured in
-- docs/verification-replication-guard.md: the read is deferred through
-- sp_executesql so that a missing object is a catchable runtime error, the
-- rows land in table variables, and the result sets are emitted whatever
-- happened.
--
-- WHERE distribution_agent LIVES IS NOT SETTLED. Microsoft documents it on
-- MSreplication_subscriptions and a reviewer placed it on
-- MSsubscription_properties. This file reads the documented one; if the column
-- is not there the guard records the error rather than failing the unit, and
-- the verification run against a real subscriber settles it.
--
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @applies bit = 0, @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

SELECT @applies = CONVERT(bit, d.is_subscribed)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);

DECLARE @subs TABLE (
    [publisher] sysname, [publisher_db] sysname, [publication] sysname NULL,
    [independent_agent] bit, [subscription_type] int,
    [distribution_agent] sysname NULL, [last_updated] smalldatetime NULL,
    [immediate_sync] bit);

DECLARE @agents TABLE (
    [id] int, [publisher] sysname, [publisher_db] sysname,
    [publication] sysname NULL, [subscription_type] int, [queue_id] sysname NULL);

IF @applies = 1
BEGIN
    BEGIN TRY
        INSERT INTO @subs
        EXEC sys.sp_executesql N'
            SELECT s.publisher, s.publisher_db, s.publication,
                   s.independent_agent, s.subscription_type,
                   s.distribution_agent, s.[Time], s.immediate_sync
            FROM dbo.MSreplication_subscriptions AS s
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents
        EXEC sys.sp_executesql N'
            SELECT a.id, a.publisher, a.publisher_db, a.publication,
                   a.subscription_type, a.queue_id
            FROM dbo.MSsubscription_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       CONVERT(int, @collected)                     AS [collected],
       @err                                         AS [error_number],
       NULLIF(@msg, N'')                            AS [error_message],
       (SELECT COUNT(*) FROM @subs)                 AS [counts.subscriptions],
       (SELECT MIN(s.[last_updated]) FROM @subs AS s) AS [observed.oldest_update],
       (SELECT MAX(s.[last_updated]) FROM @subs AS s) AS [observed.newest_update]
OPTION (RECOMPILE, MAXDOP 1);

SELECT s.[publisher], s.[publisher_db], s.[publication],
       s.[subscription_type],
       CASE s.[subscription_type] WHEN 0 THEN 'push' WHEN 1 THEN 'pull'
                                  WHEN 2 THEN 'anonymous' END AS [subscription_type_desc],
       s.[distribution_agent], s.[last_updated],
       CONVERT(int, s.[independent_agent])          AS [independent_agent],
       CONVERT(int, s.[immediate_sync])             AS [immediate_sync]
FROM @subs AS s ORDER BY s.[publisher], s.[publisher_db]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[id], a.[publisher], a.[publisher_db], a.[publication],
       a.[subscription_type], a.[queue_id]
FROM @agents AS a ORDER BY a.[id]
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] **Step 2: Lint, grammar, live run**

This task adds a file, so `queries_test.go:18` — which asserts
`len(scripts) != 58` — must be raised by one in the same commit. The slice adds
four files in all, ending at 62.

```bash
go test . -run TestEmbeddedCorpus
pwsh -File tools/verify-corpus-grammar.ps1
sqlcmd -S "[::1],11433" -U sa -P 'Str0ng!Passw0rd' -C -d master \
  -i queries/90.availability/043.replication-subscriber.sql
```

Expected: lint PASS; `resultsets 3/3 ok`; three result sets with
`applies = 0, collected = 1` and no error.

- [ ] **Step 3: Commit**

```bash
git add queries/90.availability/043.replication-subscriber.sql
git commit -m "Collecter le catalogue d'abonnement"
```

---

### Task 11: `044.replication-counters.sql`

**Files:**
- Create: `queries/90.availability/044.replication-counters.sql`

**Interfaces:**
- Consumes: nothing from earlier tasks; `@scope: instance`.
- Produces: root object plus one array.

It is its own file rather than an addition to `040` because
`sys.dm_os_performance_counters` needs `VIEW SERVER STATE` — measured,
`Msg 300` then `Msg 297` without it — and `@permissions` drives the skip gate.
Adding the requirement to `040` would cost a login without that right the four
replication flags it collects today.

- [ ] **Step 1: Write the file**

```sql
-- @scope:       instance
-- @resultsets:  root:object, counters:array
-- @permissions: CONNECT, VIEW SERVER STATE
-- @timeout:     60
--
-- The replication performance counters, live, without touching a history
-- table in a distribution database.
--
-- THIS IS A SEPARATE FILE FROM 040 ON PURPOSE. sys.dm_os_performance_counters
-- needs VIEW SERVER STATE; 040 declares only CONNECT and VIEW ANY DEFINITION
-- and succeeds on a login holding exactly that. Since @permissions drives the
-- skip gate, folding these counters into 040 would make a login without
-- VIEW SERVER STATE lose the four replication flags it collects today. A
-- thinner archive is better than a file that runs and fails.
--
-- COUNTERS ARE CUMULATIVE OR INSTANTANEOUS DEPENDING ON cntr_type, AND THIS
-- FILE DOES NOT INTERPRET THEM. cntr_value is projected with cntr_type beside
-- it so the analysis can decide; a rate computed from one sample is not a
-- rate.
--
-- The object names carry trailing spaces in this DMV, which is why the
-- comparison is on a trimmed value.
--
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @c TABLE (
    [object_name] nvarchar(128), [counter_name] nvarchar(128),
    [instance_name] nvarchar(128) NULL, [cntr_value] bigint, [cntr_type] int);

DECLARE @collected bit = 1, @err int = 0, @msg nvarchar(2048) = N'';

BEGIN TRY
    INSERT INTO @c
    EXEC sys.sp_executesql N'
        SELECT RTRIM(p.object_name), RTRIM(p.counter_name), RTRIM(p.instance_name),
               p.cntr_value, p.cntr_type
        FROM sys.dm_os_performance_counters AS p
        WHERE RTRIM(p.object_name) LIKE ''%Replication%''
        OPTION (RECOMPILE, MAXDOP 1)';
END TRY
BEGIN CATCH
    SELECT @collected = 0, @err = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
END CATCH

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @collected)                     AS [collected],
       @err                                         AS [error_number],
       NULLIF(@msg, N'')                            AS [error_message],
       (SELECT COUNT(*) FROM @c)                    AS [counts.counters]
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[object_name], c.[counter_name], c.[instance_name],
       c.[cntr_value], c.[cntr_type]
FROM @c AS c ORDER BY c.[object_name], c.[counter_name], c.[instance_name]
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] **Step 2: Lint, grammar, live run**

This task adds a file, so `queries_test.go:18` — which asserts
`len(scripts) != 58` — must be raised by one in the same commit. The slice adds
four files in all, ending at 62.

```bash
go test . -run TestEmbeddedCorpus
pwsh -File tools/verify-corpus-grammar.ps1
sqlcmd -S "[::1],11433" -U sa -P 'Str0ng!Passw0rd' -C \
  -i queries/90.availability/044.replication-counters.sql
```

Expected: `resultsets 2/2 ok`; two result sets. On a bare instance the counter
array is empty and `collected` is 1 — replication counters exist only once
replication is configured.

- [ ] **Step 3: Commit**

```bash
git add queries/90.availability/044.replication-counters.sql
git commit -m "Collecter les compteurs de performance de réplication, dans leur propre fichier"
```

---

### Task 12: `040.replication.sql` — header rewrite and the remote distributor

**Files:**
- Modify: `queries/90.availability/040.replication.sql`

**Interfaces:**
- Consumes: nothing.
- Produces: one more result set; `@resultsets` goes from 2 to 3.

- [ ] **Step 1: Replace the header's obsolete argument**

The current header argues at length that distribution metadata is out of reach
because the distribution database's name is not fixed. That argument is now
answered by 042 and the selection's second pass, so it goes. What stays: the
flags are flags and not proof of activity, and a database restored from a
publisher keeps them.

Replace the paragraphs from "WHAT THIS FILE DELIBERATELY DOES NOT DO" through
"...fails on a distributor named anything else." with:

```sql
-- WHERE THE REST OF IT IS. The interesting replication metadata lives in the
-- publication, distribution and subscription databases, and is collected by
-- 041, 042 and 043. This file keeps the instance-level answer: which databases
-- carry which role, and whether the distributor is somewhere else entirely.
--
-- The distribution database's name is chosen when replication is configured
-- and sp_adddistributiondb has no default for it — "distribution" is the name
-- the SSMS wizard suggests. Nothing here depends on that name: the selection's
-- second pass finds the database by its is_distributor flag.
```

- [ ] **Step 2: Remove the two root columns the rewrite makes false**

The file's root object carries
`'distribution_database_name_not_fixed' AS [not_collected.reason]` and
`'50.agent/010.jobs.sql' AS [not_collected.see]`. Both were true when nothing
read the distribution database. After Task 13 they are a machine-readable claim
contradicting the header above them, and the analysis layer was told to match
on the first.

Delete lines 62 to 69 as one block: the two columns and the comment paragraph
above them, which exists only to justify columns that are going. Then
`[counts.distributor]` on line 53 becomes the last projected column and **its
trailing comma must go with them**, or the statement ends in
`… AS [counts.distributor], OPTION (RECOMPILE, MAXDOP 1);` and every run of the
file returns `Msg 156, Incorrect syntax near the keyword 'OPTION'`. Measured on
the container; the grammar checker catches it too, but only after the commit.
The result:

```sql
       (SELECT COUNT(*) FROM sys.databases AS d WHERE d.is_distributor = 1)     AS [counts.distributor]
       /* No SERVERPROPERTY here, and that is a correction rather than an
          … (paragraph unchanged) … */
OPTION (RECOMPILE, MAXDOP 1);
```

The `SERVERPROPERTY` paragraph on lines 54–61 stays where it is. It records a
correction — that `IsPublisher` and `IsSubscriber` are not real property names,
and returned NULL on a live run rather than raising — which is still true and
still worth not repeating. A comment between the last column and `OPTION` is
legal; the grammar check in Step 4 confirms it.

- [ ] **Step 3: Add the remote-distributor result set**

Append, and change `@resultsets` to
`root:object, databases:array, distributor_servers:array`:

```sql
/* Where the distributor is, when it is not here. sys.servers carries the flag,
   but the row's name is the alias 'repl_distributor' rather than the server:
   the instance is in data_source. Projecting name alone would put a constant
   in the archive and call it a finding. */
SELECT s.[server_id]                                              AS [server_id],
       s.[name]                                                   AS [entry_name],
       COALESCE(NULLIF(s.[data_source], N''), s.[name])           AS [server],
       CONVERT(int, s.[is_publisher])                             AS [is_publisher],
       CONVERT(int, s.[is_subscriber])                            AS [is_subscriber],
       CONVERT(int, s.[is_distributor])                           AS [is_distributor]
FROM sys.servers AS s
WHERE s.[is_publisher] = 1 OR s.[is_subscriber] = 1 OR s.[is_distributor] = 1
ORDER BY s.[server_id]
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] **Step 4: Lint, grammar, live run**

This task modifies a file rather than adding one, so the script count in
`queries_test.go:18` does not move here.

```bash
go test . -run TestEmbeddedCorpus
pwsh -File tools/verify-corpus-grammar.ps1
sqlcmd -S "[::1],11433" -U sa -P 'Str0ng!Passw0rd' -C \
  -i queries/90.availability/040.replication.sql
```

Expected: `resultsets 3/3 ok`; three result sets, the third empty on an
instance with no replication.

- [ ] **Step 5: Commit**

```bash
git add queries/90.availability/040.replication.sql
git commit -m "Réécrire l'en-tête de 040 et nommer le distributeur distant"
```

---

### Task 13: `042.replication-distribution.sql`

**Files:**
- Create: `queries/90.availability/042.replication-distribution.sql`

**Interfaces:**
- Consumes: `@widened: replication`.
- Produces: root object plus six arrays.

Written last because it is the largest and because the pattern will have been
exercised three times by then. Its full projection list is in
`docs/replication-spec.md` under "90.availability/042"; the sections are
configuration from `msdb.dbo.MSdistributiondbs` filtered to `name = DB_NAME()`,
topology from `MSpublications`/`MSarticles`/`MSsubscriptions`, agents from the
three agent tables, profiles from `msdb.dbo.MSagent_profiles` and
`MSagent_profile_parameters`, latency aggregated per agent from
`MSdistribution_history` and `MSlogreader_history`, and the last 50 rows of
`MSrepl_errors`.

**What this file does not collect, and why that is a decision.** The
specification's section on `042` also names `MSsubscriptions` for the
publication-to-subscriber mapping, the agent profiles in
`msdb.dbo.MSagent_profiles` and `MSagent_profile_parameters`, the tracer-token
history, the cleanup agents' throughput, and the `error_id` join from the
history tables to `MSrepl_errors`. None of them is in the result sets below.

That is deliberate slicing and it was not written down until a reviewer noticed
the gap. All five need a configured topology to be worth anything — a profile
list on an instance with no agents is seven rows of defaults — and all five are
additions to an existing file rather than new plumbing. They belong to the
slice that follows the first real audit run, when the verification questions
the specification lists are answered at the same time. `@resultsets` gains its
slots then.

**`@permissions` omits `MSDB READ` although step 3 reads
`msdb.dbo.MSdistributiondbs`.** That is consistent with this specification's
posture — nothing is required, the read is attempted, and a refusal is recorded
by the handler around it — but Task 11 argues the opposite trade at length for
`sys.dm_os_performance_counters`. The difference is that losing the counters
costs a whole file, while losing the retention row costs one array inside a
file whose other six sections still land. Declaring `MSDB READ` would make the
skip gate drop all seven.

**`errors.last_message` keeps only the last failure.** Six families each have
their own error number and all six survive; the message text does not, because
one variable holds it. That is the right trade for a root object — six message
columns would be six mostly-empty strings — and the numbers are what an
analysis matches on. Worth knowing when reading an archive where two sections
failed for different reasons.

Four constraints specific to this file, each from a measurement:

- **The msdb replication tables do not exist on an instance that was never a
  distributor.** They are created by `sp_adddistributor`, not by setup. Each
  object family therefore gets its own `TRY`/`CATCH` inside the `IF`, so one
  absent family does not cost the others.
- **The two `delivery_latency` columns measure different legs** — publisher to
  distribution in `MSlogreader_history`, distribution to subscriber in
  `MSdistribution_history` — and must never be merged into one number.
- **The median is the analytic form.** `PERCENTILE_CONT(0.5) WITHIN GROUP
  (ORDER BY delivery_latency) OVER (PARTITION BY agent_id)` in a CTE, collapsed
  by a grouping outside it. The aggregate form exists in Azure SQL and Fabric
  and on no version of SQL Server — `Msg 10753` on 2022.
- **`sys.dm_db_partition_stats` needs `VIEW DATABASE STATE`.** The
  `MSrepl_commands` row count therefore gets its own `TRY`/`CATCH` and its own
  `collected` flag, so a login without that right still gets the topology.

- [ ] **Step 1: Write the header and the guard**

```sql
-- @scope:       database
-- @resultsets:  root:object, configuration:array, publications:array, articles:array, agents:array, latency:array, repl_errors:array
-- @permissions: CONNECT, VIEW ANY DEFINITION
-- @timeout:     120
-- @widened:     replication
--
-- The distribution database: what it distributes, for whom, how far behind,
-- and what it has been complaining about.
--
-- ONE TRY/CATCH PER OBJECT FAMILY, NOT ONE FOR THE FILE. The replication
-- tables in msdb are created by sp_adddistributor, not by setup, so on an
-- instance that was never a distributor they are absent — measured, zero rows
-- in msdb.sys.tables for the whole list. A single handler would let one absent
-- family cost every other section.
--
-- THE TWO delivery_latency COLUMNS ARE NOT THE SAME MEASUREMENT AND ARE NEVER
-- ADDED TOGETHER. In MSlogreader_history it is the milliseconds between a
-- command committing in the published database and arriving here. In
-- MSdistribution_history it is the milliseconds between here and the
-- subscriber. A topology that is behind is behind on one leg or the other, and
-- which one decides where to look.
--
-- THE MEDIAN IS THE ANALYTIC FORM. PERCENTILE_CONT as an aggregate — WITHIN
-- GROUP with no OVER — exists in Azure SQL and Fabric and on no version of SQL
-- Server: Msg 10753 on 2022. At the 2012 floor and everywhere else it is
-- OVER (PARTITION BY ...) in a CTE, collapsed by a grouping outside it.
--
-- THE ROW COUNT OF MSrepl_commands HAS ITS OWN HANDLER because
-- sys.dm_db_partition_stats needs VIEW DATABASE STATE, which this file does
-- not declare. A login without it still gets the topology.
--
-- NO PASSWORD COLUMN IS PROJECTED. MSlogreader_agents carries
-- publisher_password and job_password. Projections stay explicit.
--
-- SQL Server 2012 is the floor.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;

DECLARE @window_days int = 7;

DECLARE @applies bit = 0,
        @err_cfg int = 0, @err_topo int = 0, @err_agents int = 0,
        @err_hist int = 0, @err_errs int = 0, @err_size int = 0,
        @msg nvarchar(2048) = N'';

SELECT @applies = CONVERT(bit, d.is_distributor)
FROM sys.databases AS d
WHERE d.database_id = DB_ID()
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] **Step 2: Declare the staging tables**

```sql
DECLARE @cfg TABLE ([name] sysname, [min_distretention] int,
                    [max_distretention] int, [history_retention] int);

DECLARE @pubs TABLE ([publisher_id] smallint, [publisher_db] sysname,
                     [publication] sysname, [publication_id] int,
                     [publication_type] int, [retention] int,
                     [immediate_sync] bit, [independent_agent] bit);

DECLARE @arts TABLE ([publisher_id] smallint, [publisher_db] sysname,
                     [publication_id] int, [article] sysname, [article_id] int,
                     [source_owner] sysname NULL, [source_object] sysname NULL,
                     [destination_owner] sysname NULL, [destination_object] sysname NULL);

DECLARE @agents TABLE ([kind] varchar(12), [id] int, [name] nvarchar(100),
                       [publisher_db] sysname NULL, [publication] sysname NULL,
                       [subscriber_db] sysname NULL, [job_id] binary(16) NULL,
                       [local_job] bit NULL);

DECLARE @hist TABLE ([leg] varchar(40), [agent_id] int, [runstatus] int,
                     [last_time] datetime NULL, [last_duration] int NULL,
                     [last_latency_ms] int NULL, [max_latency_ms] int NULL,
                     [median_latency_ms] float NULL, [sessions] int,
                     [delivered_commands] bigint NULL,
                     [last_comment] nvarchar(512) NULL);

DECLARE @errs TABLE ([id] int, [time] datetime, [error_code] sysname NULL,
                     [error_text] nvarchar(512) NULL, [source_type_id] int NULL);

DECLARE @size TABLE ([table_name] sysname, [row_count] bigint);
```

- [ ] **Step 3: Read the configuration and the topology**

```sql
IF @applies = 1
BEGIN
    BEGIN TRY
        INSERT INTO @cfg
        EXEC sys.sp_executesql N'
            SELECT d.name, d.min_distretention, d.max_distretention, d.history_retention
            FROM msdb.dbo.MSdistributiondbs AS d
            WHERE d.name = DB_NAME()
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_cfg = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    BEGIN TRY
        INSERT INTO @pubs
        EXEC sys.sp_executesql N'
            SELECT p.publisher_id, p.publisher_db, p.publication, p.publication_id,
                   p.publication_type, p.retention, p.immediate_sync, p.independent_agent
            FROM dbo.MSpublications AS p
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @arts
        EXEC sys.sp_executesql N'
            SELECT a.publisher_id, a.publisher_db, a.publication_id, a.article,
                   a.article_id, a.source_owner, a.source_object,
                   a.destination_owner, a.destination_object
            FROM dbo.MSarticles AS a
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_topo = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
```

- [ ] **Step 4: Read the agents**

Three tables, three inserts, one handler. Only `MSdistribution_agents` has
subscriber columns — a Log Reader reads a publisher's log and a Snapshot Agent
writes files; neither talks to a subscriber, and projecting `subscriber` across
all three would fail with `Invalid column name`.

```sql
    BEGIN TRY
        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''distribution'', a.id, a.name, a.publisher_db, a.publication,
                   a.subscriber_db, a.job_id, a.local_job
            FROM dbo.MSdistribution_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''logreader'', a.id, a.name, a.publisher_db, a.publication,
                   NULL, a.job_id, a.local_job
            FROM dbo.MSlogreader_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';

        INSERT INTO @agents ([kind], [id], [name], [publisher_db], [publication],
                             [subscriber_db], [job_id], [local_job])
        EXEC sys.sp_executesql N'
            SELECT ''snapshot'', a.id, a.name, a.publisher_db, a.publication,
                   NULL, a.job_id, a.local_job
            FROM dbo.MSsnapshot_agents AS a
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_agents = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
```

- [ ] **Step 5: Aggregate the two latency legs**

```sql
    BEGIN TRY
        INSERT INTO @hist
        EXEC sys.sp_executesql N'
            WITH h AS (
                SELECT ''distribution_to_subscriber'' AS leg, x.agent_id, x.runstatus,
                       x.[time], x.duration, x.delivery_latency, x.delivered_commands,
                       x.comments,
                       PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x.delivery_latency)
                           OVER (PARTITION BY x.agent_id) AS median_latency,
                       ROW_NUMBER() OVER (PARTITION BY x.agent_id ORDER BY x.[time] DESC) AS rn
                FROM dbo.MSdistribution_history AS x
                WHERE x.[time] >= DATEADD(day, -@days, GETDATE())
                UNION ALL
                SELECT ''publisher_to_distribution'', x.agent_id, x.runstatus,
                       x.[time], x.duration, x.delivery_latency, x.delivered_commands,
                       x.comments,
                       PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY x.delivery_latency)
                           OVER (PARTITION BY x.agent_id),
                       ROW_NUMBER() OVER (PARTITION BY x.agent_id ORDER BY x.[time] DESC)
                FROM dbo.MSlogreader_history AS x
                WHERE x.[time] >= DATEADD(day, -@days, GETDATE())
            )
            SELECT h.leg, h.agent_id,
                   MAX(CASE WHEN h.rn = 1 THEN h.runstatus END),
                   MAX(CASE WHEN h.rn = 1 THEN h.[time] END),
                   MAX(CASE WHEN h.rn = 1 THEN h.duration END),
                   MAX(CASE WHEN h.rn = 1 THEN h.delivery_latency END),
                   MAX(h.delivery_latency),
                   MAX(h.median_latency),
                   COUNT(*),
                   SUM(CONVERT(bigint, h.delivered_commands)),
                   LEFT(MAX(CASE WHEN h.rn = 1 THEN h.comments END), 512)
            FROM h GROUP BY h.leg, h.agent_id
            OPTION (RECOMPILE, MAXDOP 1)',
            N'@days int', @days = @window_days;
    END TRY
    BEGIN CATCH
        SELECT @err_hist = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
```

The median is carried through the CTE by `MAX()` because it is constant within
the partition — that is how an analytic value is collapsed by a `GROUP BY`
without a second scan.

- [ ] **Step 6: Read the errors and the table size**

```sql
    BEGIN TRY
        INSERT INTO @errs
        EXEC sys.sp_executesql N'
            SELECT TOP (50) e.id, e.[time], e.error_code, LEFT(CONVERT(nvarchar(4000), e.error_text), 512),
                   e.source_type_id
            FROM dbo.MSrepl_errors AS e
            WHERE e.[time] >= DATEADD(day, -@days, GETDATE())
            ORDER BY e.[time] DESC
            OPTION (RECOMPILE, MAXDOP 1)',
            N'@days int', @days = @window_days;
    END TRY
    BEGIN CATCH
        SELECT @err_errs = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH

    /* Its own handler: sys.dm_db_partition_stats needs VIEW DATABASE STATE,
       which this file does not declare. No COUNT(*) — MSrepl_commands is the
       largest table on any busy distributor and this collector must not be the
       reason one stalls. */
    BEGIN TRY
        INSERT INTO @size
        EXEC sys.sp_executesql N'
            SELECT o.name, SUM(ps.row_count)
            FROM sys.dm_db_partition_stats AS ps
            JOIN sys.objects AS o ON o.object_id = ps.object_id
            WHERE ps.index_id IN (0, 1)
              AND o.name IN (N''MSrepl_commands'', N''MSrepl_transactions'')
            GROUP BY o.name
            OPTION (RECOMPILE, MAXDOP 1)';
    END TRY
    BEGIN CATCH
        SELECT @err_size = ERROR_NUMBER(), @msg = ERROR_MESSAGE();
    END CATCH
END
```

- [ ] **Step 7: Emit the seven result sets unconditionally**

```sql
SELECT CONVERT(varchar(23), SYSDATETIME(), 126)     AS [collected_at],
       CONVERT(int, @applies)                       AS [applies],
       /* collected, on the same terms as 041, 043 and 044: every guarded read
          returned. Six guards here rather than one, so it is their conjunction.
          A reader comparing files should not have to learn a second convention
          because this one happens to have more error codes. */
       CASE WHEN @err_cfg = 0 AND @err_topo = 0 AND @err_agents = 0
             AND @err_hist = 0 AND @err_errs = 0 AND @err_size = 0
            THEN 1 ELSE 0 END                       AS [collected],
       @window_days                                 AS [window_days],
       @err_cfg    AS [errors.configuration], @err_topo  AS [errors.topology],
       @err_agents AS [errors.agents],        @err_hist  AS [errors.history],
       @err_errs   AS [errors.repl_errors],   @err_size  AS [errors.size],
       NULLIF(@msg, N'')                            AS [errors.last_message],
       (SELECT COUNT(*) FROM @pubs)                 AS [counts.publications],
       (SELECT COUNT(*) FROM @arts)                 AS [counts.articles],
       (SELECT COUNT(*) FROM @agents)               AS [counts.agents],
       (SELECT [row_count] FROM @size WHERE [table_name] = N'MSrepl_commands')
                                                    AS [counts.repl_commands_rows],
       /* Staged by the same read as the line above, so projecting it costs
          nothing and dropping it would leave the query paying for a row it
          throws away. The pair is also the useful reading: commands per
          transaction says whether the backlog is many small units or few
          large ones. */
       (SELECT [row_count] FROM @size WHERE [table_name] = N'MSrepl_transactions')
                                                    AS [counts.repl_transactions_rows]
OPTION (RECOMPILE, MAXDOP 1);

SELECT c.[name], c.[min_distretention], c.[max_distretention], c.[history_retention]
FROM @cfg AS c OPTION (RECOMPILE, MAXDOP 1);

SELECT p.[publisher_id], p.[publisher_db], p.[publication], p.[publication_id],
       p.[publication_type],
       CASE p.[publication_type] WHEN 0 THEN 'transactional' WHEN 1 THEN 'snapshot'
                                 WHEN 2 THEN 'merge' END       AS [publication_type_desc],
       p.[retention], CONVERT(int, p.[immediate_sync])          AS [immediate_sync],
       CONVERT(int, p.[independent_agent])                      AS [independent_agent]
FROM @pubs AS p ORDER BY p.[publisher_db], p.[publication]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[publisher_db], a.[publication_id], a.[article], a.[article_id],
       a.[source_owner], a.[source_object], a.[destination_owner], a.[destination_object]
FROM @arts AS a ORDER BY a.[publisher_db], a.[article]
OPTION (RECOMPILE, MAXDOP 1);

SELECT a.[kind], a.[id], a.[name], a.[publisher_db], a.[publication],
       a.[subscriber_db], CONVERT(int, a.[local_job]) AS [local_job]
FROM @agents AS a ORDER BY a.[kind], a.[name]
OPTION (RECOMPILE, MAXDOP 1);

SELECT h.[leg], h.[agent_id], h.[runstatus],
       CASE h.[runstatus] WHEN 1 THEN 'start' WHEN 2 THEN 'succeed'
                          WHEN 3 THEN 'in progress' WHEN 4 THEN 'idle'
                          WHEN 5 THEN 'retry' WHEN 6 THEN 'fail' END AS [runstatus_desc],
       h.[last_time], h.[last_duration], h.[last_latency_ms],
       h.[max_latency_ms], h.[median_latency_ms], h.[sessions],
       h.[delivered_commands], h.[last_comment]
FROM @hist AS h ORDER BY h.[leg], h.[agent_id]
OPTION (RECOMPILE, MAXDOP 1);

SELECT e.[id], e.[time], e.[error_code], e.[error_text], e.[source_type_id]
FROM @errs AS e ORDER BY e.[time] DESC
OPTION (RECOMPILE, MAXDOP 1);
```

- [ ] **Step 8: Lint, grammar, live run**

This task adds a file, so `queries_test.go:18` — which asserts
`len(scripts) != 58` — must be raised by one in the same commit. The slice adds
four files in all, ending at 62.

```bash
go test . -run TestEmbeddedCorpus
pwsh -File tools/verify-corpus-grammar.ps1
sqlcmd -S "[::1],11433" -U sa -P 'Str0ng!Passw0rd' -C -d master \
  -i queries/90.availability/042.replication-distribution.sql
```

Expected: `resultsets 7/7 ok`; seven result sets on a bare instance. In the
root object, `applies` 0 and `collected` 1 — nothing applied and nothing
failed, which are different questions — every `errors.*` code 0,
`errors.last_message` NULL, every array empty, and both `counts.repl_*_rows`
NULL rather than 0, because on a bare instance the tables do not exist to have
a row count. **No `Msg 208`** — if one appears, a read escaped `sp_executesql`.

- [ ] **Step 9: Commit**

```bash
git add queries/90.availability/042.replication-distribution.sql
git commit -m "Collecter la base de distribution : topologie, agents, latence, erreurs"
```

---

### Task 14: The guide tells the DBA what changed for them

**Files:**
- Modify: `docs/dba-guide.md`

**Interfaces:**
- Consumes: the behaviour of Tasks 3–6 and the four collectors.
- Produces: nothing.

**Why it is a task and not a line in a commit message.** Two things changed
that a DBA reading the guide would otherwise meet for the first time in the
`check` output: a run narrowed with `DB_INCLUDE` can now collect a database the
operator did not name, and four collectors read replication metadata with
whatever rights the login happens to have. Both are exactly the kind of
surprise this guide exists to prevent, and neither is visible from the corpus.

- [ ] **Step 1: Add the widening to "Three things that are not obvious"**

That section is the right home: it is where the guide already explains
behaviour a reader would not predict from the flags. Rename its heading to
"Four things that are not obvious from the outside" and add, after the
`READ UNCOMMITTED` subsection:

```markdown
### A narrowed run may still collect the distribution database

`DB_INCLUDE` names the databases you want. There is one case where the
collector adds one you did not name: if a database you did include is a
replication publisher, and the distributor is this same instance, the
distribution database is collected too.

It is not a loophole in the filter. The publication metadata on a publisher
says what is published; everything about whether replication is *working* —
agent history, delivery latency, undistributed commands, replication errors —
lives in the distribution database, and a run that collected the first and not
the second would report a healthy topology it never measured.

Only the four replication collectors are offered that database. The thirty-odd
ordinary database collectors are not: no object inventory, no index usage, no
Query Store. And `DB_EXCLUDE` still wins — naming the distribution database
there keeps it out, at the cost of the agent and latency data.

`MANIFEST.txt` says so on the database itself, under `kept because:`, and
`check` prints the same sentence beside the folder before you authorise
anything.
```

- [ ] **Step 2: Say what replication collection costs in rights**

In "The six rights, and what each one costs when missing", after the existing
entries, add:

```markdown
**Replication metadata** asks for no right you are not already granting. The
four replication collectors read the publication, distribution and subscription
catalogs with whatever the audit login has, and each one records in its own
result set which reads returned and which were refused — `applies`, `collected`
and an error code per area. A login without `replmonitor` or membership in the
publication access list gets the instance-level answer (which databases carry
which role, where a remote distributor is) and an error code in place of the
topology. Nothing fails, and nothing is silently missing: the archive says
which of the two you are looking at.

There is deliberately no new grant to ask for here. If your organisation would
have to raise the audit login's rights to collect this, the right answer is to
run without it and read the error codes.
```

- [ ] **Step 3: Check the count in the contents block**

`docs/dba-guide.md:17` has a `### Contents` list. If it names the "Three
things" section by that number, the rename in Step 1 makes it wrong.

- [ ] **Step 4: Verify**

```bash
git grep -n "Three things that are not obvious"
```

Expected: no hits. Then reread the two additions against `MANIFEST.txt` and the
`check` output from Task 5 — the guide quotes both, and a quotation that drifts
from what the program prints is worse than no quotation.

- [ ] **Step 5: Commit**

```bash
git add docs/dba-guide.md
git commit -m "Dire au DBA qu'une sélection restreinte peut ramener la base de distribution"
```

---

## What this slice does not do

The verification questions the spec lists stay open, because none can be
answered without a configured topology: whether a bare reader gets 229 or zero
rows from `syspublications`, what `replmonitor` actually grants, which join
resolves `MSpublications.publisher_id` at the 2012 floor, where
`distribution_agent` lives, and whether 50 rows of `MSrepl_errors` is right.
Each is recorded in `docs/replication-spec.md` and each is settled by the first
real audit, not by this slice.

The `docs/dba-guide.md` paragraphs are Task 14, which is inside this slice: a
behaviour change the operator meets first in `check` output is not
documentation that can wait for the next one.
