# Collection profiles: implementation plan

> For agentic workers: required sub-skill, superpowers:subagent-driven-development (recommended) or superpowers:executing-plans, task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

Goal: implement `--profile space` for `check`, `collect` and the wizard, the opt-in `70.schema/055.page-density.sql` collector behind `--measure-page-density`, and the widened table listing of `010.objects` and `060.columns`, exactly as `docs/profiles-spec.md` specifies.

Architecture: a closed `@profiles` directive on collectors; a profile gate evaluated first in `skipReason`; refusals in `optionsFrom`, repeated as guards in `VerifyLocal` and `Run`; a `not_needed` preflight status applied where statuses are read; a profile block in `_run.json` and `MANIFEST.txt`; the replication widening driven by the plan; wizard state that carries the profile.

Tech stack: Go (standard library, `testing`, `testing/fstest`), T-SQL collectors embedded with `//go:embed`, SQL Server 2025 in the Podman container `sql2025` for the SQL verification steps.

Spec: `docs/profiles-spec.md` at commit `6531af9`. Read it before your task; this plan argues from it, and where the two disagree, stop and say so.

## Global constraints

- This repository is public. No client identifier anywhere: code, SQL comments, tests, fixtures, docs, commit messages, file names. Use `SQL01`, `SQL01\PROD`, `SALESDB`, `192.0.2.0/24`, `example.com`.
- Commit messages are in English. The body is prose that explains why. No `Co-Authored-By`, no `Generated with`, no attribution trailer of any kind.
- Documentation you write (README, docs, CHANGELOG) uses no bold and no em dash or en dash.
- `go test ./...` must pass before every commit.
- `testdata/corpus.txt` is regenerated with `go test . -run TestEmbeddedCorpusIsValid -update`, never edited by hand and never regenerated in CI. Read its diff before committing.
- A header comment line of a collector must not begin with an `@` word: the parser reads it as a directive.
- Profile name: `space`. Skip reason: `not in profile space`. Preflight status: `not_needed`. Flag: `measure_page_density`, option `--measure-page-density`. JSON block: `"profile": {"name", "members", "corpus"}`.
- Every setup and its test run happen in ONE shell invocation. Shell state does not survive between tool calls, so a database created in one call and queried in the next may be a different world.
- Every `go test -run` below names its expected count of top-level tests. Count the `--- PASS` / `--- FAIL` lines. A lower count means the filter is wrong, not that the task is done.
- Every task that adds an assertion has a break step: break the behaviour, confirm the named test fails, restore. Reporting "two of three broke as predicted" is the successful outcome of that step. Keep a copy of the file outside the repository to restore it; never `git checkout`, `git restore`, `git stash` or `git clean` a file holding uncommitted work.
- If your own measurement contradicts this plan or the spec, stop and say so in your report rather than making the code match the brief.
- SQL verification runs only against `sql2025` (`localhost,11533`). Its `sa` password stays in the container: `podman exec sql2025 printenv MSSQL_SA_PASSWORD | ... --password-stdin`, and T-SQL through `podman exec -i sql2025 bash -c '/opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P "$MSSQL_SA_PASSWORD" -C -b -i /dev/stdin'`. Databases you create are named `review_<something>` and dropped before you finish. Never create logins, jobs or Extended Events sessions; never touch another database.

---

### Task 1: the `@profiles` directive

Files:
- Modify: `collect/queryset.go` (the `Script` struct, `knownDirectives`, the `switch key` in `parseScript`, beside `KnownDisclosures` and `knownDisclosureNames`)
- Test: `collect/queryset_test.go`

Interfaces:
- Produces: `type Profile struct{ Description string }`, `var KnownProfiles map[string]Profile`, `func knownProfileNames() []string`, field `Script.Profiles []string`.

- [ ] Step 1: write the failing tests

Add to `collect/queryset_test.go`:

```go
func TestDiscoverParsesProfiles(t *testing.T) {
	body := "-- @scope:       instance\n-- @resultsets:  a:object\n-- @timeout:     60\n" +
		"-- @profiles:    space, SPACE\n" + contractPreamble +
		"SELECT 1 AS [x] OPTION (RECOMPILE, MAXDOP 1);\n"
	fsys := fstest.MapFS{
		"queries/10.system/010.a.sql": {Data: []byte(body)},
		"queries/10.system/020.b.sql": {Data: []byte(goodSQL)},
	}
	got, err := Discover(fsys, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d scripts, want 2", len(got))
	}
	// A repeated name, whatever its case, is one membership.
	if len(got[0].Profiles) != 1 || got[0].Profiles[0] != "space" {
		t.Errorf("Profiles = %v, want [space]", got[0].Profiles)
	}
	if len(got[1].Profiles) != 0 {
		t.Errorf("a script without the directive belongs to no profile, got %v", got[1].Profiles)
	}
}
```

Add two rows to the table of `TestDiscoverLintErrors`:

```go
		// A misspelt profile would silently leave the collector out of every
		// profiled run, which is the failure a closed vocabulary exists for.
		{"unknown profile", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\n-- @profiles: spaec\nSELECT 1;", "spaec"},
		{"a profiles directive naming nothing", "queries/10.system/010.a.sql",
			"-- @resultsets: a:object\n-- @profiles: ,\nSELECT 1;", "no profile named"},
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestDiscoverParsesProfiles|TestDiscoverLintErrors' -v`
Expected: 2 top-level tests. `TestDiscoverParsesProfiles` fails to compile or reports empty Profiles; the two new subtests of `TestDiscoverLintErrors` fail ("unknown directive @profiles" does not contain "spaec" is acceptable only if the substring is missing; read the message).

- [ ] Step 3: implement

In `collect/queryset.go`, add to `Script` after `Discloses []string`:

```go
	// Profiles names the profiles this collector belongs to, in the vocabulary
	// of KnownProfiles. Empty means the collector runs only when no profile is
	// requested. A profile only ever removes collectors from a run.
	Profiles []string
```

Add after `knownDisclosureNames`:

```go
// Profile describes one named subset of the corpus. Description is what
// check, MANIFEST.txt and the wizard print beside the name.
type Profile struct {
	Description string
}

// KnownProfiles is the closed set of names @profiles accepts, for the reason
// KnownFlags is closed: a misspelt name would silently leave a collector out
// of every profiled run.
var KnownProfiles = map[string]Profile{
	"space": {Description: "what makes the databases on this instance larger " +
		"than they need to be: index usage and size, compression, page fullness, " +
		"files, logs and tempdb"},
}

func knownProfileNames() []string {
	names := make([]string, 0, len(KnownProfiles))
	for n := range KnownProfiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

Append `"profiles"` to `knownDirectives`, after `"discloses"`.

Add a case to the `switch key` in `parseScript`, after `case "discloses":`:

```go
		case "profiles":
			named := 0
			for _, part := range strings.Split(val, ",") {
				name := strings.ToLower(strings.TrimSpace(part))
				if name == "" {
					continue
				}
				named++
				if _, ok := KnownProfiles[name]; !ok {
					setLint(fmt.Sprintf("@profiles: unknown value %q; expected one of %s",
						strings.TrimSpace(part), strings.Join(knownProfileNames(), ", ")))
					break
				}
				if !slices.Contains(s.Profiles, name) {
					s.Profiles = append(s.Profiles, name)
				}
			}
			if named == 0 {
				setLint("@profiles: no profile named")
			}
```

`slices` and `sort` are already imported by `queryset.go` (the `discloses` case uses `slices.Contains`); confirm with `go build ./collect`.

- [ ] Step 4: run and watch them pass

Run: `go test ./collect -run 'TestDiscoverParsesProfiles|TestDiscoverLintErrors' -v`
Expected: 2 top-level tests, both PASS.

- [ ] Step 5: break it

Copy `collect/queryset.go` to `/tmp/qs.go.bak`. (a) Remove the `if named == 0` block: the "a profiles directive naming nothing" subtest must fail. (b) Restore, then replace `KnownProfiles[name]` lookup with `true` (accept any name): the "unknown profile" subtest must fail. (c) Restore, then drop the `!slices.Contains` guard: `TestDiscoverParsesProfiles` must fail on `[space space]`. Restore from the copy and rerun Step 4. Report how many of the three bit.

- [ ] Step 6: full suite and commit

Run: `go test ./...` (all packages ok).

```bash
git add collect/queryset.go collect/queryset_test.go
git commit -m "collect: add the @profiles directive" -m "A collection profile is declared by the collectors themselves, the way an opt-in flag is. The vocabulary is closed because a misspelt profile name would otherwise leave a collector out of every profiled run with nothing saying so."
```

### Task 2: the `space` members and the corpus inventory

Files:
- Modify: the 20 collectors below (one header line each)
- Modify: `corpus_golden_test.go` (`checkCorpusInventory`, `readCorpusGolden`)
- Modify: `queries_test.go` (new test)
- Regenerate: `testdata/corpus.txt`

Interfaces:
- Consumes: `collect.Script.Profiles`, `collect.KnownProfiles` (Task 1).
- Produces: corpus.txt line format `<path>` or `<path> @profiles: <names comma-joined>`.

- [ ] Step 1: write the failing test

Add to `queries_test.go`:

```go
// TestEveryKnownProfileHasACollector compares two sets decided in different
// places: the names @profiles may say, and the names the embedded corpus does
// say. A profile nobody declares would refuse every run that asks for it.
func TestEveryKnownProfileHasACollector(t *testing.T) {
	scripts, err := collect.Discover(sqlauditor.Queries, "queries")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	declared := map[string]bool{}
	for _, s := range scripts {
		for _, p := range s.Profiles {
			declared[p] = true
		}
	}
	for name := range collect.KnownProfiles {
		if !declared[name] {
			t.Errorf("profile %q is known and no embedded collector declares it", name)
		}
	}
}
```

- [ ] Step 2: run and watch it fail

Run: `go test . -run TestEveryKnownProfileHasACollector -v`
Expected: 1 test, FAIL, `profile "space" is known and no embedded collector declares it`.

- [ ] Step 3: declare the members

Insert the line `-- @profiles:    space` immediately after the last directive line of each file. Line numbers measured on `6531af9`:

| File | Insert after line |
| --- | --- |
| `queries/10.system/010.properties.sql` | 4 |
| `queries/10.system/030.file-io.sql` | 4 |
| `queries/10.system/050.tempdb.sql` | 4 |
| `queries/10.system/051.version-store.sql` | 5 |
| `queries/20.databases/010.all-databases.sql` | 4 |
| `queries/20.databases/020.properties.sql` | 4 |
| `queries/20.databases/022.query-store.sql` | 5 |
| `queries/20.databases/023.log-vlf.sql` | 4 |
| `queries/20.databases/024.log-stats.sql` | 5 |
| `queries/50.agent/010.jobs.sql` | 4 |
| `queries/50.agent/020.job-steps.sql` | 5 |
| `queries/50.agent/040.maintenance-plans.sql` | 4 |
| `queries/60.backup/010.history.sql` | 4 |
| `queries/70.schema/010.objects.sql` | 4 |
| `queries/70.schema/020.index-usage.sql` | 4 |
| `queries/70.schema/040.compression.sql` | 4 |
| `queries/70.schema/041.compression-savings.sql` | 5 |
| `queries/70.schema/050.heaps.sql` | 4 |
| `queries/70.schema/060.columns.sql` | 4 |
| `queries/70.schema/070.index-columns.sql` | 4 |

Verify: `grep -l '^-- @profiles:    space$' queries -r | wc -l` prints 20, and `head -7` of each shows the line inside the directive block, not after the first prose line.

- [ ] Step 4: record membership in the inventory

In `corpus_golden_test.go`, replace the construction of `got` in `checkCorpusInventory`:

```go
	got := make([]string, 0, len(scripts))
	for _, s := range scripts {
		got = append(got, inventoryLine(s))
	}
	sort.Strings(got)
```

and add:

```go
// inventoryLine is the path, followed by the profiles the collector declares
// when it declares any. Membership is in the golden file so that a collector
// cannot enter or leave a profile without the diff saying so.
func inventoryLine(s collect.Script) string {
	if len(s.Profiles) == 0 {
		return s.Path
	}
	p := append([]string(nil), s.Profiles...)
	sort.Strings(p)
	return s.Path + " @profiles: " + strings.Join(p, ", ")
}

// splitInventoryLine undoes inventoryLine.
func splitInventoryLine(line string) (path, profiles string) {
	path, profiles, _ = strings.Cut(line, " @profiles: ")
	return path, profiles
}
```

Replace the two comparison loops (after the `want` lines are read) with:

```go
	wantProfiles := map[string]string{}
	for _, l := range want {
		p, prof := splitInventoryLine(l)
		wantProfiles[p] = prof
	}
	gotProfiles := map[string]string{}
	for _, l := range got {
		p, prof := splitInventoryLine(l)
		gotProfiles[p] = prof
	}
	for p, prof := range gotProfiles {
		w, ok := wantProfiles[p]
		switch {
		case !ok:
			t.Errorf("%s is in the corpus and not in %s: a new collector, or a rename "+
				"whose other half is below", p, corpusGolden)
		case w != prof:
			t.Errorf("%s: profiles %q in the corpus, %q in %s", p, prof, w, corpusGolden)
		}
	}
	for p := range wantProfiles {
		if _, ok := gotProfiles[p]; !ok {
			t.Errorf("%s is in %s and not in the corpus: a collector was removed, renamed, "+
				"or is no longer embedded", p, corpusGolden)
		}
	}
```

Keep the existing comments above those loops; they still hold.

- [ ] Step 5: regenerate and read the diff

Run: `go test . -run TestEmbeddedCorpusIsValid -update -v && git diff --stat testdata/corpus.txt && git diff testdata/corpus.txt | grep '^[-+]' | grep -v '^[-+][-+]' | wc -l`
Expected: the diff changes exactly 20 lines out and 20 lines in (40 lines counted), each gaining ` @profiles: space`.

- [ ] Step 6: run the tests

Run: `go test . -run 'TestEmbeddedCorpusIsValid|TestEveryKnownProfileHasACollector' -v`
Expected: 2 tests, both PASS.

- [ ] Step 7: break it

Copy `queries/70.schema/050.heaps.sql` to `/tmp/`. Remove its `@profiles` line and rerun Step 6 without `-update`: `TestEmbeddedCorpusIsValid` must fail with `70.schema/050.heaps.sql: profiles "" in the corpus, "space" in testdata/corpus.txt`. Restore. Then edit `testdata/corpus.txt` by hand to delete one ` @profiles: space` suffix: the same test must fail in the other direction. Restore with `go test . -run TestEmbeddedCorpusIsValid -update` and confirm `git diff testdata/corpus.txt` shows only the 20 intended changes. Report both.

- [ ] Step 8: full suite and commit

Run: `go test ./...`

```bash
git add queries corpus_golden_test.go queries_test.go testdata/corpus.txt
git commit -m "queries: declare the space profile on its twenty members" -m "The space question needs a known set of existing collectors, chosen in docs/profiles-spec.md. Membership is recorded in the corpus inventory so that a collector cannot enter or leave a profile without a reviewer seeing it in the diff."
```

### Task 3: the profile gate in the plan

Files:
- Create: `collect/profile.go`
- Create: `collect/profile_test.go`
- Modify: `collect/collect.go` (`Options`, `skipReason`, `planScripts`, the `planScripts` call in `Run`)
- Modify: `collect/verify.go` (the `planScripts` call in `VerifyServer`)
- Modify: every existing test call of `skipReason` or `planScripts` the compiler lists (`collect/collect_test.go` has four `skipReason` tests)

Interfaces:
- Consumes: `Script.Profiles` (Task 1).
- Produces: `Options.Profile string`; `func ProfileSkipReason(profile string) string`; `func skipReason(s Script, profile string, denied map[string]bool, serverVersion []int, enabled map[string]bool) (string, bool)`; `func planScripts(scripts []Script, profile string, denied map[string]bool, serverVersion []int, enabled map[string]bool) []plannedScript`.

- [ ] Step 1: write the failing tests

Create `collect/profile_test.go`:

```go
package collect

import (
	"strings"
	"testing"
)

func TestSkipReasonForProfile(t *testing.T) {
	member := Script{Path: "70.schema/050.heaps.sql", Profiles: []string{"space"}}
	outsider := Script{Path: "80.workload/010.wait-stats.sql",
		RequiresFlag: FlagIncludeSessionText, Permissions: []string{"view_server_state"}}

	// The operator's choice is reported first: a flag or a permission would
	// send them to an option that changes nothing for this run.
	reason, skip := skipReason(outsider, "space", map[string]bool{"view_server_state": true}, nil, nil)
	if !skip || reason != "not in profile space" {
		t.Errorf("outsider: skip=%v reason=%q, want the profile reason", skip, reason)
	}
	if _, skip := skipReason(member, "space", nil, nil, nil); skip {
		t.Error("a member with no other gate must run")
	}
	if _, skip := skipReason(outsider, "", nil, nil, map[string]bool{FlagIncludeSessionText: true}); skip {
		t.Error("without a profile, nothing changes for a script whose flag is on")
	}
	gated := Script{Path: "70.schema/041.compression-savings.sql",
		Profiles: []string{"space"}, RequiresFlag: FlagEstimateCompression}
	reason, skip = skipReason(gated, "space", nil, nil, nil)
	if !skip || !strings.Contains(reason, "--estimate-compression") {
		t.Errorf("a gated member falls through to its flag: skip=%v reason=%q", skip, reason)
	}
}

func TestPlanScriptsAppliesTheProfile(t *testing.T) {
	plan := planScripts([]Script{
		{Path: "a.sql", Profiles: []string{"space"}},
		{Path: "b.sql"},
	}, "space", nil, nil, nil)
	if plan[0].Skip != "" || plan[1].Skip != ProfileSkipReason("space") {
		t.Errorf("plan = %+v", plan)
	}
}
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestSkipReasonForProfile|TestPlanScriptsAppliesTheProfile' -v`
Expected: the package does not compile (`too many arguments`, `undefined: ProfileSkipReason`). That is the failure.

- [ ] Step 3: implement

Create `collect/profile.go`:

```go
package collect

import "slices"

// ProfileSkipReason is the one place the sentence is built, so MANIFEST.txt
// can recognise these skips by equality rather than by searching the text.
func ProfileSkipReason(profile string) string {
	return "not in profile " + profile
}

// inProfile reports whether a script belongs to the profile. An empty profile
// is the whole corpus.
func inProfile(s Script, profile string) bool {
	return profile == "" || slices.Contains(s.Profiles, profile)
}
```

In `collect/collect.go`, add to `Options` after `Flags`:

```go
	// Profile is the requested collection profile, "" for the whole corpus.
	// It only removes collectors: see skipReason.
	Profile string
```

Change `skipReason` to take `profile string` as its second parameter, and put this first in its body, before the `RequiresFlag` check:

```go
	if !inProfile(s, profile) {
		return ProfileSkipReason(profile), true
	}
```

Extend its comment's first paragraph: "The profile comes first of all, because choosing it is the operator's choice and a later gate would name an option that changes nothing for this run."

Change `planScripts` to take `profile string` as its second parameter and pass it to `skipReason`. In `Run`, change the call to `planScripts(scripts, o.Profile, denied, ParseVersion(si.Version), o.Flags)`. In `VerifyServer`, change it to `planScripts(v.Scripts, o.Profile, denied, ParseVersion(si.Version), o.Flags)`. Fix every test call the compiler reports by inserting `""` as the second argument.

- [ ] Step 4: run and watch them pass

Run: `go test ./collect -run 'TestSkipReason|TestPlanScriptsAppliesTheProfile' -v`
Expected: 6 top-level tests (the four existing `TestSkipReasonFor*`, `TestSkipReasonForProfile`, `TestPlanScriptsAppliesTheProfile`), all PASS.

- [ ] Step 5: break it

Copy `collect/collect.go` aside. (a) Move the profile check after the `RequiresFlag` check: `TestSkipReasonForProfile` must fail on the outsider's reason. (b) Restore, then make `inProfile` return `true` always: both new tests must fail. Restore and rerun Step 4.

- [ ] Step 6: full suite and commit

Run: `go test ./...`

```bash
git add collect
git commit -m "collect: skip collectors outside the requested profile" -m "The plan is the single decision both check and collect read, so the profile gate lives there and is evaluated before the flag, version and permission gates: telling an operator to pass an option for a collector the profile removed would send them to a switch that changes nothing."
```

### Task 4: `CheckProfile`

Files:
- Modify: `collect/profile.go`
- Test: `collect/profile_test.go`

Interfaces:
- Consumes: `KnownProfiles`, `knownProfileNames` (Task 1), `KnownFlags`.
- Produces: `func CheckProfile(scripts []Script, profile string, flags map[string]bool) error`; `func ProfileMembers(scripts []Script, profile string) []Script` (lint-clean scripts in the profile; the input unchanged when `profile == ""`).

- [ ] Step 1: write the failing test

Append to `collect/profile_test.go` (add `"errors"` is not needed; `strings` is already imported):

```go
func TestCheckProfile(t *testing.T) {
	member := Script{Path: "70.schema/050.heaps.sql", Profiles: []string{"space"}}
	gated := Script{Path: "70.schema/041.compression-savings.sql", Profiles: []string{"space"},
		RequiresFlag: FlagEstimateCompression}
	broken := Script{Path: "70.schema/099.broken.sql", Profiles: []string{"space"},
		RequiresFlag: FlagQueryStoreDetail, LintError: "@timeout: missing"}
	outsider := Script{Path: "80.workload/021.query-store-detail.sql", RequiresFlag: FlagQueryStoreDetail}

	cases := []struct {
		name    string
		scripts []Script
		profile string
		flags   map[string]bool
		want    string // "" means no error
	}{
		{"no profile is never refused", []Script{outsider}, "", map[string]bool{FlagQueryStoreDetail: true}, ""},
		{"unknown name", []Script{member}, "spaec", nil, `unknown profile "spaec"; expected one of space`},
		{"no declaring script", []Script{outsider}, "space", nil,
			"profile space has no collector in this corpus; a corpus exported before profiles existed declares none"},
		{"every declaring script failed lint", []Script{{Path: "x.sql", Profiles: []string{"space"}, LintError: "bad"}}, "space", nil,
			"profile space has no usable collector in this corpus: x.sql failed lint (bad)"},
		{"a flag with no member", []Script{member, outsider}, "space", map[string]bool{FlagQueryStoreDetail: false, FlagIncludeSessionText: true},
			"--include-session-text has no collector in profile space, so it would collect nothing; drop the option or the profile"},
		{"a flag whose only member failed lint", []Script{member, broken}, "space", map[string]bool{FlagQueryStoreDetail: true},
			"--query-store-detail has no usable collector in profile space: 70.schema/099.broken.sql failed lint (@timeout: missing)"},
		{"a flag with a member", []Script{member, gated}, "space", map[string]bool{FlagEstimateCompression: true}, ""},
		{"a flag that is off is not judged", []Script{member}, "space", map[string]bool{FlagQueryStoreDetail: false}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckProfile(c.scripts, c.profile, c.flags)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("unexpected refusal: %v", err)
			case c.want != "" && (err == nil || err.Error() != c.want):
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestProfileMembers(t *testing.T) {
	all := []Script{
		{Path: "a.sql", Profiles: []string{"space"}},
		{Path: "b.sql"},
		{Path: "c.sql", Profiles: []string{"space"}, LintError: "bad"},
	}
	if got := ProfileMembers(all, ""); len(got) != 3 {
		t.Errorf("without a profile the corpus is returned as given, got %d", len(got))
	}
	got := ProfileMembers(all, "space")
	if len(got) != 1 || got[0].Path != "a.sql" {
		t.Errorf("members = %+v, want only a.sql", got)
	}
}
```

- [ ] Step 2: run and watch it fail

Run: `go test ./collect -run 'TestCheckProfile|TestProfileMembers' -v`
Expected: compile failure, `undefined: CheckProfile`.

- [ ] Step 3: implement

Append to `collect/profile.go`, and change its import to `import ("fmt"; "slices"; "sort"; "strings")` in block form:

```go
// ProfileMembers returns the lint-clean scripts that declare the profile, or
// scripts unchanged when no profile is requested.
func ProfileMembers(scripts []Script, profile string) []Script {
	if profile == "" {
		return scripts
	}
	var out []Script
	for _, s := range scripts {
		if s.LintError == "" && slices.Contains(s.Profiles, profile) {
			out = append(out, s)
		}
	}
	return out
}

// CheckProfile refuses the profile and flag combinations that would produce a
// run which looks successful and collects nothing the operator asked for. It
// judges declarations and lint only. A member that the instance's version or
// the login's rights will gate off is not known here; it is skipped at planning
// and recorded in skipped_scripts, as a flagged collector too recent for the
// instance is today.
func CheckProfile(scripts []Script, profile string, flags map[string]bool) error {
	if profile == "" {
		return nil
	}
	if _, ok := KnownProfiles[profile]; !ok {
		return fmt.Errorf("unknown profile %q; expected one of %s",
			profile, strings.Join(knownProfileNames(), ", "))
	}
	var members, failed []Script
	for _, s := range scripts {
		if !slices.Contains(s.Profiles, profile) {
			continue
		}
		if s.LintError != "" {
			failed = append(failed, s)
			continue
		}
		members = append(members, s)
	}
	if len(members) == 0 {
		if len(failed) > 0 {
			return fmt.Errorf("profile %s has no usable collector in this corpus: %s",
				profile, lintFailures(failed))
		}
		return fmt.Errorf("profile %s has no collector in this corpus; "+
			"a corpus exported before profiles existed declares none", profile)
	}
	names := make([]string, 0, len(flags))
	for name, on := range flags {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if slices.ContainsFunc(members, func(s Script) bool { return s.RequiresFlag == name }) {
			continue
		}
		option := KnownFlags[name]
		if option == "" {
			option = name
		}
		var gatedFailed []Script
		for _, s := range failed {
			if s.RequiresFlag == name {
				gatedFailed = append(gatedFailed, s)
			}
		}
		if len(gatedFailed) > 0 {
			return fmt.Errorf("%s has no usable collector in profile %s: %s",
				option, profile, lintFailures(gatedFailed))
		}
		return fmt.Errorf("%s has no collector in profile %s, so it would collect nothing; "+
			"drop the option or the profile", option, profile)
	}
	return nil
}

func lintFailures(scripts []Script) string {
	parts := make([]string, 0, len(scripts))
	for _, s := range scripts {
		parts = append(parts, fmt.Sprintf("%s failed lint (%s)", s.Path, s.LintError))
	}
	return strings.Join(parts, "; ")
}
```

- [ ] Step 4: run and watch it pass

Run: `go test ./collect -run 'TestCheckProfile|TestProfileMembers' -v`
Expected: 2 top-level tests, PASS, with 8 subtests under `TestCheckProfile`.

- [ ] Step 5: break it

Copy `collect/profile.go` aside. (a) Delete the `if on` filter so every key of `flags` is judged: "a flag that is off is not judged" must fail. (b) Restore, then delete the `gatedFailed` branch: "a flag whose only member failed lint" must fail. (c) Restore, then delete the `len(failed) > 0` branch in the zero-member case: "every declaring script failed lint" must fail. Restore, rerun Step 4, report the count that bit.

- [ ] Step 6: full suite and commit

Run: `go test ./...`

```bash
git add collect/profile.go collect/profile_test.go
git commit -m "collect: refuse profile combinations that would collect nothing" -m "A profile with no member in the corpus, or an opt-in flag with no member in the profile, would otherwise produce an archive and exit 0 while holding nothing the operator asked for. When lint is what emptied the set, the refusal names the lint failure, because that is the repair."
```

### Task 5: the profile in `_run.json` and `MANIFEST.txt`

Files:
- Modify: `collect/manifest.go` (`Manifest`, `Human`, `writeNotRun`, new `ProfileBlock`, new `profileLine`)
- Modify: `collect/profile.go` (new `profileCounts`)
- Modify: `collect/collect.go` (`Run`: set the name after `NewManifest`, the counts after `Discover`)
- Test: `collect/profile_test.go`

Interfaces:
- Consumes: `ProfileSkipReason`, `inProfile` (Task 3).
- Produces: `type ProfileBlock struct { Name string \`json:"name"\`; Members int \`json:"members"\`; Corpus int \`json:"corpus"\` }`; field `Manifest.Profile ProfileBlock` with JSON key `profile`; `func profileCounts(scripts []Script, profile string) (members, corpus int)`.

- [ ] Step 1: write the failing tests

Append to `collect/profile_test.go` (add imports `context`, `encoding/json`, `os`, `path/filepath`, `testing/fstest`, `time`):

```go
func TestManifestHumanProfileLine(t *testing.T) {
	cases := []struct {
		block ProfileBlock
		want  string
	}{
		{ProfileBlock{}, "Profile : none, the whole corpus"},
		{ProfileBlock{Name: "space", Members: 21, Corpus: 84}, "Profile : space, 21 of the 84 collectors in the corpus belong to it"},
		{ProfileBlock{Name: "space"}, "Profile : space, requested; the corpus was not read"},
	}
	for _, c := range cases {
		m := &Manifest{Profile: c.block}
		if h := flatten(m.Human()); !strings.Contains(h, c.want) {
			t.Errorf("MANIFEST.txt does not say %q:\n%s", c.want, m.Human())
		}
	}
}

func TestManifestHumanGroupsProfileSkips(t *testing.T) {
	m := &Manifest{Profile: ProfileBlock{Name: "space", Members: 2, Corpus: 5}}
	m.Skipped = []SkippedScript{
		{Script: "80.workload/010.wait-stats.sql", Reason: ProfileSkipReason("space")},
		{Script: "80.workload/020.query-store.sql", Reason: ProfileSkipReason("space")},
		{Script: "70.schema/041.compression-savings.sql", Reason: "not collected by default; pass --estimate-compression to include it"},
	}
	h := m.Human()
	if !strings.Contains(h, "Queries not run (3):") {
		t.Errorf("the heading must count every entry of skipped_scripts:\n%s", h)
	}
	if !strings.Contains(h, "  - 2 collectors outside profile space, each listed in _run.json") {
		t.Errorf("the profile skips must collapse into one line:\n%s", h)
	}
	if strings.Contains(h, "80.workload/010.wait-stats.sql") {
		t.Errorf("a collapsed skip must not also be listed:\n%s", h)
	}
	if !strings.Contains(h, "70.schema/041.compression-savings.sql") {
		t.Errorf("a skip for another reason must still be listed:\n%s", h)
	}
}

func TestRunRecordsTheRequestedProfileWhenTheCorpusCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "output")
	missing := filepath.Join(dir, "no-such-corpus")
	if _, err := Run(context.Background(), Options{
		Config:  &Config{Server: "localhost", OutputDir: out, QueriesDir: missing},
		Corpus:  os.DirFS(missing),
		Root:    ".",
		Now:     time.Now(),
		Profile: "space",
	}); err == nil {
		t.Fatal("want an error")
	}
	b, err := os.ReadFile(filepath.Join(failedRunDir(t, out), "_run.json"))
	if err != nil {
		t.Fatalf("no manifest written: %v", err)
	}
	var got struct {
		Profile ProfileBlock `json:"profile"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Profile != (ProfileBlock{Name: "space"}) {
		t.Errorf("profile block = %+v, want the requested name and no counts", got.Profile)
	}
}

func TestProfileCounts(t *testing.T) {
	scripts := []Script{
		{Path: "a.sql", Profiles: []string{"space"}},
		{Path: "b.sql"},
		{Path: "c.sql", Profiles: []string{"space"}, LintError: "bad"},
	}
	if m, c := profileCounts(scripts, "space"); m != 1 || c != 2 {
		t.Errorf("members, corpus = %d, %d, want 1, 2", m, c)
	}
	if m, c := profileCounts(scripts, ""); m != 0 || c != 2 {
		t.Errorf("without a profile: members, corpus = %d, %d, want 0, 2", m, c)
	}
}
```

`flatten` is defined in `collect/manifest_test.go` and `failedRunDir` in `collect/collect_test.go`; both are in package `collect` and usable here.

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestManifestHumanProfileLine|TestManifestHumanGroupsProfileSkips|TestRunRecordsTheRequestedProfile|TestProfileCounts' -v`
Expected: compile failure, `undefined: ProfileBlock`.

- [ ] Step 3: implement

In `collect/manifest.go`, add before `type Manifest struct`:

```go
// ProfileBlock says which profile produced the archive. It is written on every
// run, with an empty Name when none was requested, for the reason the
// transport block is: an absent key and a key saying "none" must not be told
// apart by guesswork.
type ProfileBlock struct {
	// Name is the profile requested, empty when none was.
	Name string `json:"name"`
	// Members is how many lint-clean scripts of the corpus declare the
	// profile, 0 without one; Corpus is how many lint-clean scripts the
	// corpus holds. Both are 0 when the run ended before the corpus was read.
	// What actually ran is in results.
	Members int `json:"members"`
	Corpus  int `json:"corpus"`
}
```

Add to `Manifest`, after `Config`: `Profile ProfileBlock \`json:"profile"\``.

In `Human`, right after the `Contents` line, add `fmt.Fprintf(&b, "Profile      : %s\n", m.profileLine())`, and add:

```go
func (m *Manifest) profileLine() string {
	switch {
	case m.Profile.Name == "":
		return "none, the whole corpus"
	case m.Profile.Corpus == 0:
		return m.Profile.Name + ", requested; the corpus was not read"
	default:
		return fmt.Sprintf("%s, %d of the %d collectors in the corpus belong to it",
			m.Profile.Name, m.Profile.Members, m.Profile.Corpus)
	}
}
```

Replace the body of `writeNotRun` with:

```go
	if len(m.Skipped) == 0 {
		return
	}
	fmt.Fprintf(b, "\nQueries not run (%d):\n", len(m.Skipped))
	// The profile's skips collapse into one line placed first. A security
	// officer reads this document, and sixty identical lines bury the skips
	// that carry information. _run.json keeps every entry.
	profiled := 0
	if m.Profile.Name != "" {
		reason := ProfileSkipReason(m.Profile.Name)
		for _, s := range m.Skipped {
			if s.Reason == reason {
				profiled++
			}
		}
		if profiled > 0 {
			fmt.Fprintf(b, "  - %d collectors outside profile %s, each listed in _run.json\n",
				profiled, m.Profile.Name)
		}
	}
	for _, s := range m.Skipped {
		if profiled > 0 && s.Reason == ProfileSkipReason(m.Profile.Name) {
			continue
		}
		if s.Target != "" {
			fmt.Fprintf(b, "  - %s on %s\n      %s\n", s.Script, s.Target, s.Reason)
			continue
		}
		fmt.Fprintf(b, "  - %s\n      %s\n", s.Script, s.Reason)
	}
```

In `collect/profile.go`, add:

```go
// profileCounts returns how many lint-clean scripts declare the profile (0
// without one) and how many lint-clean scripts the corpus holds.
func profileCounts(scripts []Script, profile string) (members, corpus int) {
	for _, s := range scripts {
		if s.LintError != "" {
			continue
		}
		corpus++
		if profile != "" && slices.Contains(s.Profiles, profile) {
			members++
		}
	}
	return members, corpus
}
```

In `Run`, right after `m := NewManifest(...)`, add `m.Profile.Name = o.Profile` with the comment "Set before anything can fail, so every failed-run record says what was asked." Right after the successful `Discover` in `Run`, add `m.Profile.Members, m.Profile.Corpus = profileCounts(scripts, o.Profile)`.

- [ ] Step 4: run and watch them pass

Run: `go test ./collect -run 'TestManifestHumanProfileLine|TestManifestHumanGroupsProfileSkips|TestRunRecordsTheRequestedProfile|TestProfileCounts' -v`
Expected: 4 tests, PASS.

- [ ] Step 5: the existing manifest tests

Run: `go test ./collect -run 'Manifest|Human|Coverage' -v 2>&1 | grep -c '^--- '`
Every `Human()` test in the repository asserts with `strings.Contains`, so none should need a change; if one fails, it asserted an exact line order: update it to the new text and say which in your report.

- [ ] Step 6: break it

Copy `collect/manifest.go` and `collect/collect.go` aside. (a) Remove `m.Profile.Name = o.Profile` from `Run`: `TestRunRecordsTheRequestedProfileWhenTheCorpusCannotBeRead` must fail. (b) Restore, then remove the `continue` that skips collapsed entries: `TestManifestHumanGroupsProfileSkips` must fail. (c) Restore, then swap the first two cases of `profileLine`: `TestManifestHumanProfileLine` must fail. Restore and rerun Step 4.

- [ ] Step 7: full suite and commit

Run: `go test ./...`

```bash
git add collect
git commit -m "collect: record the profile in _run.json and MANIFEST.txt" -m "An archive has to say which profile produced it, or the analysis layer reads every collector the profile removed as a collector that failed. The name is recorded before anything can fail so that a failed-run record still says what was asked, and the profile's skips collapse into one line of MANIFEST.txt while _run.json keeps each of them."
```

### Task 6: the profile in the run folder name

Files:
- Modify: `collect/output.go` (`RunFolderName`)
- Modify: `collect/collect.go` (`RunFolderFor`, its call in `Run`)
- Modify: `tui/state.go` (`runFolderFor`)
- Test: `collect/output_test.go`, `collect/collect_test.go`, `tui/state_test.go` (signature updates)

Interfaces:
- Produces: `func RunFolderName(server, profile string, t time.Time) string`; `func RunFolderFor(outputDir, server, profile string, now time.Time, keep bool) string`.

- [ ] Step 1: write the failing tests

In `collect/output_test.go`, replace `TestRunFolderName` with:

```go
func TestRunFolderName(t *testing.T) {
	ts := time.Date(2026, 8, 8, 9, 5, 0, 0, time.UTC)
	if got := RunFolderName(`SRV01\INST`, "", ts); got != "SRV01_INST-2026-08-08" {
		t.Errorf("RunFolderName without a profile = %q", got)
	}
	// A full run and a space run on the same day must not replace each other.
	if got := RunFolderName(`SRV01\INST`, "space", ts); got != "SRV01_INST-2026-08-08-space" {
		t.Errorf("RunFolderName with a profile = %q", got)
	}
}
```

In `collect/collect_test.go`, add:

```go
func TestRunFolderForPutsTheKeepSuffixAfterTheProfile(t *testing.T) {
	dir := t.TempDir()
	now, err := time.Parse(time.RFC3339, "2026-08-08T13:45:00Z")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "SRV01-2026-08-08-space")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := RunFolderFor(dir, "SRV01", "space", now, true); got != base+"-1345" {
		t.Errorf("got %q, want %q", got, base+"-1345")
	}
	if got := RunFolderFor(dir, "SRV01", "", now, true); got != filepath.Join(dir, "SRV01-2026-08-08") {
		t.Errorf("a full run is not in the way of a space run: got %q", got)
	}
}
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestRunFolderName|TestRunFolderFor' -v`
Expected: compile failure, `too many arguments in call to RunFolderName`.

- [ ] Step 3: implement

```go
func RunFolderName(server, profile string, t time.Time) string {
	name := fmt.Sprintf("%s-%s", SafeFolderName(server), t.Format("2006-01-02"))
	if profile != "" {
		name += "-" + profile
	}
	return name
}
```

Give `RunFolderFor` the `profile string` parameter after `server` and pass it to `RunFolderName`. In `Run`: `RunFolderFor(o.Config.OutputDir, si.Name, o.Profile, o.Now, o.Keep)`. In `tui/state.go` `runFolderFor`: `collect.RunFolderFor(o.Config.OutputDir, s.Verify.Server.Name, "", o.Now, o.Keep)` for now; Task 12 replaces `""` with `s.Profile`. Update every test call the compiler reports (`collect/collect_test.go` lines around 526 to 578, `tui/state_test.go` around 327 to 345) by inserting `""`.

- [ ] Step 4: run and watch them pass

Run: `go test ./collect ./tui -run 'TestRunFolderName|TestRunFolderFor|TestKeepRunsNeverLandOnAnExistingRun|TestTheRunFolderIsNamed|TestTheSameDayArchiveIsDetected' -v`
Expected: 7 top-level tests across the two packages, all PASS.

- [ ] Step 5: break it

Copy `collect/output.go` aside and drop the `if profile != ""` block: both new tests must fail. Restore.

- [ ] Step 6: full suite and commit

Run: `go test ./...`

```bash
git add collect tui
git commit -m "collect: name the run folder after its profile" -m "A full run and a space run of the same instance on the same day are different archives, and a shared folder name would make the second replace the first."
```

### Task 7: `--profile` on the command line, and the guards

Files:
- Modify: `cmd/sql-auditor/main.go` (`cliFlags`, `defineFlags`, `optionsFrom`, `usage` text)
- Modify: `collect/verify.go` (`VerifyResult`, `VerifyLocal`)
- Modify: `collect/collect.go` (`Check`, `Run`)
- Test: `cmd/sql-auditor/main_test.go`, `collect/profile_test.go`

Interfaces:
- Consumes: `CheckProfile` (Task 4), `Options.Profile` (Task 3), `m.Profile` (Task 5).
- Produces: `VerifyResult.ProfileErr error`; `Options.Profile` filled from `--profile`.

Where things are: `run()` in `main.go` parses the flag set and calls `optionsFrom(c, os.Getenv, os.Stdin, dbg)` (around line 741); `buildOptionsWithDebug` calls the same `optionsFrom` (line 261). `buildOptions` alone is reached only by tests and the wizard, so the refusals go in `optionsFrom`. `run()` prints the version banner before calling `optionsFrom`; a refusal is therefore not silent, and nothing in this task changes that. `main.go` does not import `errors`: use `fmt.Errorf`.

- [ ] Step 1: write the failing tests

Append to `cmd/sql-auditor/main_test.go` (it already imports `os`, `path/filepath`, `strings`, `testing`):

```go
func TestProfileIsParsedIntoOptions(t *testing.T) {
	env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\n")
	o, code, err := buildOptions("collect", []string{"--env", env, "--profile", "space"}, noEnv, noStdin)
	if err != nil || code != 0 {
		t.Fatalf("buildOptions: code %d, err %v", code, err)
	}
	if o.Profile != "space" {
		t.Errorf("Profile = %q, want space", o.Profile)
	}
}

func TestProfileRefusals(t *testing.T) {
	oldCorpus := t.TempDir()
	if err := os.MkdirAll(filepath.Join(oldCorpus, "10.system"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldCorpus, "10.system", "010.a.sql"),
		[]byte("-- @resultsets: a:object\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"--all with --profile", []string{"--all", "--profile", "space"}, "--all and --profile cannot be combined"},
		{"unknown profile", []string{"--profile", "spaec"}, `unknown profile "spaec"`},
		{"an option with no member", []string{"--profile", "space", "--query-store-detail"},
			"--query-store-detail has no collector in profile space"},
		{"a corpus exported before profiles", []string{"--profile", "space", "--queries-dir", oldCorpus},
			"a corpus exported before profiles existed declares none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := writeDotEnv(t, "SQL_SERVER=invalid.invalid\n")
			_, code, err := buildOptions("collect", append([]string{"--env", env}, c.args...), noEnv, noStdin)
			if code != 2 || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("code %d, err %v; want exit 2 with %q", code, err, c.want)
			}
		})
	}
}
```

Append to `collect/profile_test.go` (add imports `io` if not present):

```go
// captureStdout runs f with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

// noMemberCorpus holds one valid collector that declares no profile.
func noMemberCorpus() fstest.MapFS {
	body := "-- @scope:       instance\n-- @resultsets:  a:object\n-- @permissions: VIEW SERVER STATE\n-- @timeout:     60\n" +
		contractPreamble + "SELECT 1 AS [x] OPTION (RECOMPILE, MAXDOP 1);\n"
	return fstest.MapFS{"queries/10.system/010.a.sql": {Data: []byte(body)}}
}

func TestCheckRefusesTheProfileBeforeListing(t *testing.T) {
	var code int
	var err error
	printed := captureStdout(t, func() {
		code, err = Check(context.Background(), Options{
			Config:  &Config{Server: "localhost", OutputDir: t.TempDir()},
			Corpus:  noMemberCorpus(),
			Root:    "queries",
			Profile: "space",
		})
	})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "declares none") {
		t.Errorf("code %d, err %v; want 2 with the no-member refusal", code, err)
	}
	if strings.Contains(printed, "Queries (") {
		t.Errorf("the listing was printed before the refusal:\n%s", printed)
	}
}

func TestRunRefusesTheProfileAfterDiscoveryAndRecordsIt(t *testing.T) {
	out := filepath.Join(t.TempDir(), "output")
	code, err := Run(context.Background(), Options{
		Config:  &Config{Server: "localhost", OutputDir: out},
		Corpus:  noMemberCorpus(),
		Root:    "queries",
		Now:     time.Now(),
		Profile: "space",
	})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "declares none") {
		t.Fatalf("code %d, err %v; want 2 with the no-member refusal", code, err)
	}
	b, rerr := os.ReadFile(filepath.Join(failedRunDir(t, out), "_run.json"))
	if rerr != nil {
		t.Fatalf("no failed-run record: %v", rerr)
	}
	var got struct {
		Profile ProfileBlock `json:"profile"`
	}
	if jerr := json.Unmarshal(b, &got); jerr != nil {
		t.Fatal(jerr)
	}
	if got.Profile.Name != "space" || got.Profile.Corpus != 1 || got.Profile.Members != 0 {
		t.Errorf("profile block = %+v, want name space, corpus 1, members 0", got.Profile)
	}
}
```

`Run` reaches `Open` only after this refusal, so the test needs no server. If it hangs on a connection, the guard is in the wrong place: that is a finding, report it.

- [ ] Step 2: run and watch them fail

Run: `go test ./cmd/sql-auditor -run 'TestProfileIsParsedIntoOptions|TestProfileRefusals' -v; go test ./collect -run 'TestCheckRefusesTheProfileBeforeListing|TestRunRefusesTheProfileAfterDiscovery' -v`
Expected: `flag provided but not defined: -profile` exits the first test binary (the flag set uses `ExitOnError`), and the two `collect` tests fail (Check prints the listing; Run gets past discovery).

- [ ] Step 3: implement the command line

In `cliFlags`, add `profile string` beside `to, grantScript`. In `defineFlags`, after the `--all` flag:

```go
	// A profile only removes collectors. It is refused beside --all, which
	// asks for the opposite, and the refusal is in optionsFrom.
	fs.StringVar(&c.profile, "profile", "",
		"collect only the collectors of this profile: space")
```

At the very top of `optionsFrom`, before `readPassword`:

```go
	// Before anything else, because it needs no corpus and no configuration.
	if c.all && c.profile != "" {
		return collect.Options{}, 2, fmt.Errorf("--all and --profile cannot be combined: " +
			"--all asks for the widest archive this tool can produce, and a profile for a narrow one")
	}
```

Just before the final `return opts, 0, nil` of `optionsFrom` (after the `QueriesDir` block that sets `opts.Corpus`):

```go
	opts.Profile = c.profile
	// Only when a profile was asked for, so a run without one, and the wizard,
	// which never receives one on its command line, do no extra work. A corpus
	// that cannot be read is not judged here: Run and Check report that error
	// as they always have.
	if opts.Profile != "" {
		dbg.printf("checking profile %s against the corpus", opts.Profile)
		if scripts, derr := collect.Discover(opts.Corpus, opts.Root); derr == nil {
			if perr := collect.CheckProfile(scripts, opts.Profile, opts.Flags); perr != nil {
				return collect.Options{}, 2, perr
			}
		}
	}
```

In `usage()`, add after the `--keep` entry, aligned like its neighbours:

```
  --profile NAME              collect only the collectors of a profile. The one
                              profile is space: what makes the databases on this
                              instance larger than they need to be. It removes
                              collectors and never adds one; an opt-in option
                              still needs to be given. Refused beside --all.
```

- [ ] Step 4: implement the guards

In `collect/verify.go`, add to `VerifyResult` beside `CorpusErr`:

```go
	// ProfileErr is a refused profile or flag combination, found once the
	// corpus is known. Check returns 2 with it before printing anything.
	ProfileErr error
```

In `VerifyLocal`, right after the loop that counts `LintFailures`:

```go
	if perr := CheckProfile(scripts, o.Profile, o.Flags); perr != nil {
		v.ProfileErr = perr
		return v, perr
	}
```

In `Check`, right after the `if v.CorpusErr != nil { return 2, err }` block:

```go
	if v.ProfileErr != nil {
		return 2, v.ProfileErr
	}
```

In `Run`, right after the counts line added in Task 5 (after `Discover`):

```go
	// The command line refuses this first; this guard is for a caller that
	// builds Options itself. It leaves a failed-run record naming the request.
	if err := CheckProfile(scripts, o.Profile, o.Flags); err != nil {
		m.Errors = append(m.Errors, ErrorEntry{Message: err.Error()})
		return finishWith("", 2, err)
	}
```

- [ ] Step 5: run and watch them pass

Run: `go test ./cmd/sql-auditor -run 'TestProfileIsParsedIntoOptions|TestProfileRefusals' -v && go test ./collect -run 'TestCheckRefusesTheProfileBeforeListing|TestRunRefusesTheProfileAfterDiscovery' -v`
Expected: 2 tests (4 subtests) in `cmd/sql-auditor`, 2 tests in `collect`, all PASS.

- [ ] Step 6: break it

Copy the three files aside. (a) Remove the `--all` check from `optionsFrom`: its subtest must fail (the run would now be refused later, or accepted; read which). (b) Restore, then remove the `CheckProfile` call in `optionsFrom`: the three other subtests must fail. (c) Restore, then remove the `ProfileErr` branch in `Check`: `TestCheckRefusesTheProfileBeforeListing` must fail on the printed listing. (d) Restore, then remove the guard in `Run`: `TestRunRefusesTheProfileAfterDiscovery` must fail; if it hangs trying to connect to `localhost`, stop it with the `-timeout 60s` flag and count it as bitten. Restore everything, rerun Step 5, report the count.

- [ ] Step 7: full suite and commit

Run: `go test ./...`

```bash
git add cmd/sql-auditor collect
git commit -m "cmd: add --profile and refuse combinations that collect nothing" -m "The refusals live in optionsFrom because both the real command line and the wizard's option resolution go through it; buildOptions alone is never reached by the command line. Check and Run repeat the check as guards for callers that build their options themselves, and Run's refusal leaves a failed-run record that names the profile that was asked for."
```

### Task 8: the replication widening follows the plan

Files:
- Modify: `collect/runner.go` (`SelectTargets`)
- Modify: `collect/profile.go` (`WideningPurposes`, `membershipPurposes`)
- Modify: `collect/collect.go` (`Run`: build the plan before selecting)
- Modify: `collect/verify.go` (`VerifyServer`: same)
- Test: `collect/runner_test.go` (13 existing calls), `collect/profile_test.go`

Interfaces:
- Consumes: `planScripts` with profile (Task 3), `inProfile` (Task 3).
- Produces: `func SelectTargets(c []DatabaseInfo, include, exclude string, widen map[string]bool) (Selection, error)`; `func WideningPurposes(plan []plannedScript) map[string]bool`; `func membershipPurposes(scripts []Script, profile string, flags map[string]bool) map[string]bool`.

Why the order can change: in `Run`, the preflight (`collect.go` around 1422), the denied capabilities (1428) and the version probe (1433) all come before `SelectTargets` (1486), and `planScripts` depends on none of the selection. In `VerifyServer`, the probes (`verify.go` 134 and 136) come before `SelectTargets` (149). The plan can therefore be built first in both.

- [ ] Step 1: write the failing tests

In `collect/runner_test.go`, add at the top level `var widenReplication = map[string]bool{"replication": true}` and add the fourth argument `widenReplication` to all 13 existing `SelectTargets(...)` calls, so their meaning is unchanged. Then add:

```go
func TestSelectTargetsWidensOnlyForAPurpose(t *testing.T) {
	cands := []DatabaseInfo{
		{Name: "SALESDB", State: "ONLINE", HasAccess: true, IsPublished: true},
		{Name: "DISTDB", State: "ONLINE", HasAccess: true, IsDistributor: true},
	}
	with, err := SelectTargets(cands, "SALESDB", "", widenReplication)
	if err != nil {
		t.Fatal(err)
	}
	without, err := SelectTargets(cands, "SALESDB", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(with.Included, "DISTDB") {
		t.Errorf("with the purpose, DISTDB must be widened in; Included = %v", with.Included)
	}
	// No collector that will run reads the distribution database, so listing
	// it as covered would describe data the archive does not hold.
	if slices.Contains(without.Included, "DISTDB") {
		t.Errorf("without the purpose, DISTDB must not be widened in; Included = %v", without.Included)
	}
	if !slices.Contains(without.Included, "SALESDB") {
		t.Errorf("the publisher stays selected either way; Included = %v", without.Included)
	}
}
```

Append to `collect/profile_test.go`:

```go
func TestWideningPurposesFollowsThePlan(t *testing.T) {
	repl := Script{Path: "90.availability/042.replication-distribution.sql", Widened: "replication"}
	plain := Script{Path: "70.schema/010.objects.sql"}
	if got := WideningPurposes([]plannedScript{{Script: repl}, {Script: plain}}); !got["replication"] {
		t.Errorf("a planned replication collector must widen, got %v", got)
	}
	skipped := []plannedScript{{Script: repl, Skip: ProfileSkipReason("space")}, {Script: plain}}
	if got := WideningPurposes(skipped); got["replication"] {
		t.Errorf("a skipped replication collector must not widen, got %v", got)
	}
}

func TestMembershipPurposes(t *testing.T) {
	repl := Script{Path: "r.sql", Widened: "replication"}
	gated := Script{Path: "g.sql", Widened: "replication", Profiles: []string{"space"}, RequiresFlag: FlagQueryStoreDetail}
	if got := membershipPurposes([]Script{repl}, "", nil); !got["replication"] {
		t.Errorf("without a profile, an ungated replication collector widens, got %v", got)
	}
	if got := membershipPurposes([]Script{repl}, "space", nil); got["replication"] {
		t.Errorf("a replication collector outside the profile must not widen, got %v", got)
	}
	if got := membershipPurposes([]Script{gated}, "space", nil); got["replication"] {
		t.Errorf("a member whose flag is off must not widen, got %v", got)
	}
	if got := membershipPurposes([]Script{gated}, "space", map[string]bool{FlagQueryStoreDetail: true}); !got["replication"] {
		t.Errorf("a member whose flag is on widens, got %v", got)
	}
}
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestSelectTargets|TestWideningPurposesFollowsThePlan|TestMembershipPurposes' -v`
Expected: compile failure on the fourth argument and the undefined functions.

- [ ] Step 3: implement

In `SelectTargets`, add the parameter `widen map[string]bool` and, right after the first pass loop and before `published := 0`:

```go
	// The second pass exists for a collector that will read the distribution
	// database. When no such collector will run, widening would list a
	// database as covered that nothing reads.
	if !widen["replication"] {
		return sel, nil
	}
```

Extend the comment of `SelectTargets` with one sentence naming `widen`. In `collect/profile.go`:

```go
// WideningPurposes returns the @widened purposes of the planned scripts that
// will run, that is, those planScripts did not skip.
func WideningPurposes(plan []plannedScript) map[string]bool {
	out := map[string]bool{}
	for _, p := range plan {
		if p.Skip == "" && p.Script.LintError == "" && p.Script.Widened != "" {
			out[p.Script.Widened] = true
		}
	}
	return out
}

// membershipPurposes is the fallback when no plan can be built because the
// version probe failed: the purposes of the lint-clean scripts in the profile
// whose flag, if any, is on. It is the most a selection without a version can
// know.
func membershipPurposes(scripts []Script, profile string, flags map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, s := range scripts {
		if s.LintError != "" || s.Widened == "" || !inProfile(s, profile) {
			continue
		}
		if s.RequiresFlag != "" && !flags[s.RequiresFlag] {
			continue
		}
		out[s.Widened] = true
	}
	return out
}
```

In `Run`, move the line `plan := planScripts(scripts, o.Profile, denied, ParseVersion(si.Version), o.Flags)` from after `m.Targets = ...` to just before `o.Debugf("listing the databases")`, and change the selection to `SelectTargets(cands, o.Config.DBInclude, o.Config.DBExclude, WideningPurposes(plan))`. Everything that used `plan` below still finds it.

In `VerifyServer`, just before `cands, cerr := candidatesWithDeadline(...)`, add:

```go
	var plan []plannedScript
	widen := membershipPurposes(v.Scripts, o.Profile, o.Flags)
	if v.Probed {
		denied := DeniedCapabilities(v.Checks)
		// connect is not a per-script gate. Getting here means it answered.
		delete(denied, "connect")
		plan = planScripts(v.Scripts, o.Profile, denied, ParseVersion(si.Version), o.Flags)
		widen = WideningPurposes(plan)
	}
```

pass `widen` to `SelectTargets`, and replace the final `if v.Probed { ... }` block with:

```go
	if v.Probed {
		v.Collectors = countCollectors(plan)
	}
```

keeping its comment. Fix any other `SelectTargets` call the compiler reports (a test helper may call it) by passing `map[string]bool{"replication": true}`.

- [ ] Step 4: run and watch them pass

Run: `go test ./collect -run 'TestSelectTargets|TestWideningPurposesFollowsThePlan|TestMembershipPurposes' -v`
Expected: 14 top-level tests: the 11 existing `TestSelectTargets*`, `TestSelectTargetsWidensOnlyForAPurpose`, `TestWideningPurposesFollowsThePlan` and `TestMembershipPurposes`, all PASS. If the count differs, list the names you got.

- [ ] Step 5: break it

Copy `runner.go` and `profile.go` aside. (a) Remove the `if !widen["replication"]` guard: `TestSelectTargetsWidensOnlyForAPurpose` must fail. (b) Restore, then drop `p.Skip == ""` from `WideningPurposes`: `TestWideningPurposesFollowsThePlan` must fail. (c) Restore, then drop the `RequiresFlag` check from `membershipPurposes`: `TestMembershipPurposes` must fail. Restore and rerun Step 4.

- [ ] Step 6: full suite and commit

Run: `go test ./...`

```bash
git add collect
git commit -m "collect: widen to the distributor only for a collector that will run" -m "The second pass of the database selection brought the distribution database back whenever a publisher was selected, even under a profile that keeps no replication collector, and the manifest then listed it as covered while nothing read it. Everything the plan needs is known before the selection, so the widening now follows the plan; when the version probe failed, it follows profile membership."
```

### Task 9: the `not_needed` status, coverage and the `check` permissions

Files:
- Modify: `collect/preflight.go` (new `StatusNotNeeded`, `ProfileChecks`)
- Modify: `collect/manifest.go` (`refreshCoverage`, `writeCoverage`)
- Modify: `collect/collect.go` (`Run` after the preflight; `Check` after `VerifyServer`)
- Test: `collect/profile_test.go`

Interfaces:
- Consumes: `slices` (add the import to `preflight.go`), `m.Profile.Name` (Task 5).
- Produces: `const StatusNotNeeded = "not_needed"`; `func ProfileChecks(checks []CapabilityCheck, scripts []Script, profile string) []CapabilityCheck`.

The rule, from the spec: only `denied` is rewritten; never `error` (a dropped connection marks every later probe `error`, and the three probes `space` does not need are the last three, so rewriting `error` would let `check` exit 0 after losing the instance); never `connect`; never `view_any_definition` (the database discovery reads through it whatever the collectors declare). Every reader of `CapabilityCheck.Status` in the tree: `collect/preflight.go` (`RunPreflight`, `DeniedCapabilities`, `PreflightExitCode`), `collect/manifest.go` (`refreshCoverage`, `writeCoverage`, `checkStatus`), `collect/collect.go` (the `Permissions:` loop in `Check`, `anyDenied`), `collect/grants.go` (Task 10), `tui/render.go` (Task 12).

- [ ] Step 1: write the failing tests

Append to `collect/profile_test.go`:

```go
func TestProfileChecks(t *testing.T) {
	checks := []CapabilityCheck{
		{Name: "connect", Status: "denied"},
		{Name: "view_any_definition", Status: "denied"},
		{Name: "view_server_state", Status: "denied"},
		{Name: "msdb_read", Status: "ok"},
		{Name: "agent_alerts", Status: "denied"},
		{Name: "log_shipping", Status: "error"},
	}
	scripts := []Script{
		{Path: "a.sql", Profiles: []string{"space"}, Permissions: []string{"connect", "view_server_state"}},
		{Path: "b.sql", Permissions: []string{"agent_alerts", "log_shipping"}},
		{Path: "c.sql", Profiles: []string{"space"}, Permissions: []string{"agent_alerts"}, LintError: "bad"},
	}
	got := ProfileChecks(checks, scripts, "space")
	want := map[string]string{
		"connect":             "denied",     // never rewritten
		"view_any_definition": "denied",     // never rewritten: database discovery needs it
		"view_server_state":   "denied",     // a member declares it
		"msdb_read":           "ok",         // untouched
		"agent_alerts":        StatusNotNeeded, // only a non-member and a lint failure declare it
		"log_shipping":        "error",      // an unanswered probe is never rewritten
	}
	for _, c := range got {
		if c.Status != want[c.Name] {
			t.Errorf("%s = %q, want %q", c.Name, c.Status, want[c.Name])
		}
	}
	if checks[4].Status != "denied" {
		t.Error("ProfileChecks must not modify its input")
	}
	for i, c := range ProfileChecks(checks, scripts, "") {
		if c != checks[i] {
			t.Errorf("without a profile the checks come back unchanged, %s = %q", c.Name, c.Status)
		}
	}
}

func TestCoverageUnderAProfile(t *testing.T) {
	m := &Manifest{Profile: ProfileBlock{Name: "space", Members: 21, Corpus: 84},
		Preflight: []CapabilityCheck{
			{Name: "connect", Status: "ok"},
			{Name: "agent_alerts", Label: "Read the Agent alerts and operators (msdb.dbo.sysalerts)", Status: StatusNotNeeded},
		}}
	m.refreshCoverage()
	if m.Coverage.Status != "complete" {
		t.Errorf("coverage = %q, want complete: the only refusal is not needed", m.Coverage.Status)
	}
	h := flatten(m.Human())
	if !strings.Contains(h, "COMPLETE") || strings.Contains(h, "INCOMPLETE") {
		t.Errorf("MANIFEST.txt must say COMPLETE:\n%s", m.Human())
	}
	if !strings.Contains(h, "Not needed by profile space") ||
		!strings.Contains(h, "Read the Agent alerts and operators") {
		t.Errorf("MANIFEST.txt must list the unneeded right by its label:\n%s", m.Human())
	}

	m.Preflight = append(m.Preflight, CapabilityCheck{Name: "log_shipping", Status: "error"})
	m.refreshCoverage()
	if m.Coverage.Status != "incomplete" {
		t.Errorf("coverage = %q, want incomplete: an unanswered probe is still unanswered", m.Coverage.Status)
	}
}
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestProfileChecks|TestCoverageUnderAProfile' -v`
Expected: compile failure, `undefined: ProfileChecks`.

- [ ] Step 3: implement the rule

In `collect/preflight.go` (add `"slices"` to its imports):

```go
// StatusNotNeeded marks a capability the login was refused and that no member
// of the requested profile declares. It exists so that a space run prepared
// with exactly the rights it needs is not reported incomplete.
const StatusNotNeeded = "not_needed"

// ProfileChecks returns checks with the status of every capability that is
// "denied", that no member of the profile declares in @permissions, and that
// is neither "connect" nor "view_any_definition", set to "not_needed". Every
// other status, "error" included, is left as it is. With an empty profile it
// returns checks unchanged. The input is never modified.
//
// "error" is never rewritten: RunPreflight marks every probe after a lost
// connection "error", and rewriting those would hide the loss. connect is
// needed by every run, and view_any_definition by the database discovery,
// whatever the collectors declare.
func ProfileChecks(checks []CapabilityCheck, scripts []Script, profile string) []CapabilityCheck {
	if profile == "" {
		return checks
	}
	needed := map[string]bool{}
	for _, s := range scripts {
		if s.LintError == "" && slices.Contains(s.Profiles, profile) {
			for _, p := range s.Permissions {
				needed[p] = true
			}
		}
	}
	out := make([]CapabilityCheck, len(checks))
	copy(out, checks)
	for i, c := range out {
		if c.Status == "denied" && !needed[c.Name] && c.Name != "connect" && c.Name != "view_any_definition" {
			out[i].Status = StatusNotNeeded
		}
	}
	return out
}
```

In `collect/manifest.go`, `refreshCoverage`: change `if chk.Status == "ok" {` to `if chk.Status == "ok" || chk.Status == StatusNotNeeded {`. In `writeCoverage`, in the INCOMPLETE loop, change `if chk.Status == "ok" {` the same way. Then, after the closing brace of the `switch m.Coverage.Status`, and before `if m.Coverage.DatabaseListMayBeIncomplete`, add:

```go
	var notNeeded []string
	for _, chk := range m.Preflight {
		if chk.Status != StatusNotNeeded {
			continue
		}
		name := chk.Label
		if name == "" {
			name = chk.Name
		}
		notNeeded = append(notNeeded, name)
	}
	if len(notNeeded) > 0 {
		fmt.Fprintf(b, "\nNot needed by profile %s: the login was refused these, and no collector\n", m.Profile.Name)
		b.WriteString("of this profile reads what they allow.\n")
		for _, n := range notNeeded {
			fmt.Fprintf(b, "  - %s\n", n)
		}
	}
```

- [ ] Step 4: apply it where statuses are read

In `Run`, right after the `if PreflightExitCode(m.Preflight, 0, true) == 1 { ... }` block and before `denied := DeniedCapabilities(m.Preflight)`:

```go
	// Once, here: coverage, the plan's denied set and the manifest all read
	// the profiled statuses from now on. PreflightExitCode above has already
	// seen the raw ones, and "error" is never rewritten anyway.
	m.Preflight = ProfileChecks(m.Preflight, scripts, o.Profile)
```

In `Check`, right after `o.Debugf("the instance has been probed")`:

```go
	// VerifyServer returns the raw statuses; check reads them through the
	// profile, for the listing, the grant script, the advice and the exit code.
	checks := ProfileChecks(v.Checks, v.Scripts, o.Profile)
```

Replace `v.Checks` by `checks` in the `Permissions:` loop, in the `writeGrantScript(...)` call, in `anyDenied(...)` and in the final `PreflightExitCode(...)`. In the `Permissions:` loop, add a case before the default print:

```go
		if c.Status == StatusNotNeeded {
			fmt.Printf("  not needed  %s\n", c.Name)
			continue
		}
```

- [ ] Step 5: run and watch them pass

Run: `go test ./collect -run 'TestProfileChecks|TestCoverageUnderAProfile|Coverage' -v`
Expected: every test matching `Coverage` in `collect/manifest_test.go` (5 of them) plus the 2 new ones, 7 top-level tests, all PASS.

- [ ] Step 6: break it

Copy `preflight.go` and `manifest.go` aside. (a) Change `c.Status == "denied"` to `c.Status != "ok"` in `ProfileChecks`: `TestProfileChecks` must fail on `log_shipping`. (b) Restore, then remove the `view_any_definition` exclusion: it must fail on that line. (c) Restore, then drop `|| chk.Status == StatusNotNeeded` from `refreshCoverage`: `TestCoverageUnderAProfile` must fail. (d) Restore, then delete the `Not needed by profile` paragraph: the same test must fail on its second assertion. Restore, rerun Step 5, report the count.

- [ ] Step 7: full suite and commit

Run: `go test ./...`

```bash
git add collect
git commit -m "collect: mark refusals a profile does not need as not_needed" -m "A login prepared with exactly the rights a space run uses is still refused the others, and without a status of its own that refusal would print INCOMPLETE on the manifest a security officer reads. Only a refusal is rewritten: a probe that got no answer, and the two capabilities every run and the database discovery rely on, keep their status, or a dropped connection would pass for success."
```

### Task 10: the grant script under a profile

Files:
- Modify: `collect/grants.go` (`GrantScriptInput`, `BuildGrantScript` no-access condition, `writeGrantHeader`)
- Modify: `collect/collect.go` (`writeGrantScript` call in `Check`, which Task 9 already switched to `checks`)
- Test: `collect/grants_test.go`

Interfaces:
- Consumes: `StatusNotNeeded`, `ProfileChecks` (Task 9), `ProfileMembers` (Task 4).
- Produces: `GrantScriptInput.Profile string`.

- [ ] Step 1: write the failing tests

Append to `collect/grants_test.go`:

```go
func TestGrantScriptLeavesANotNeededRightAlone(t *testing.T) {
	in := baseInput("view_server_state")
	for i := range in.Checks {
		if in.Checks[i].Name == "agent_alerts" {
			in.Checks[i].Status = StatusNotNeeded
		}
	}
	in.Profile = "space"
	body, has := BuildGrantScript(in)
	if !has {
		t.Fatal("view_server_state was denied, so there is something to grant")
	}
	stmts := statements(body)
	if strings.Contains(stmts, "sysalerts") || strings.Contains(stmts, "USE msdb") {
		t.Errorf("a right the profile does not need must not be granted:\n%s", stmts)
	}
	if !strings.Contains(body, "not needed") {
		t.Errorf("the header must mark the unneeded right:\n%s", body)
	}
}

func TestGrantScriptHeaderChecksWithTheProfile(t *testing.T) {
	in := baseInput("view_server_state")
	in.Profile = "space"
	body, _ := BuildGrantScript(in)
	if !strings.Contains(body, "sql-auditor check --profile space") {
		t.Errorf("the check command must carry the profile, or the unneeded rights print denied again:\n%s", body)
	}
	if !strings.Contains(body, `"ok" or "not needed"`) {
		t.Errorf("the header must say what a successful check looks like under a profile:\n%s", body)
	}
	plain, _ := BuildGrantScript(baseInput("view_server_state"))
	if strings.Contains(plain, "--profile") {
		t.Errorf("without a profile the header must not mention one:\n%s", plain)
	}
}

func TestNoAccessSectionUnderAProfileNeedsADatabaseScopedScript(t *testing.T) {
	in := baseInput()
	in.NoAccessDatabases = []string{"SALESDB"}
	in.Profile = "space"
	instanceOnly, _ := BuildGrantScript(in)
	if strings.Contains(statements(instanceOnly), "SALESDB") {
		t.Errorf("no script of the profile enters a database, so none must be granted:\n%s", instanceOnly)
	}
	in.Scripts = append(in.Scripts, Script{Path: "queries/70.schema/020.index-usage.sql",
		Scope: ScopeDatabase, Permissions: []string{"connect"}})
	withDatabase, _ := BuildGrantScript(in)
	if !strings.Contains(statements(withDatabase), "SALESDB") {
		t.Errorf("a database-scoped member needs the database:\n%s", withDatabase)
	}
	noProfile := baseInput()
	noProfile.NoAccessDatabases = []string{"SALESDB"}
	unchanged, _ := BuildGrantScript(noProfile)
	if !strings.Contains(statements(unchanged), "SALESDB") {
		t.Errorf("without a profile the section is written as today:\n%s", unchanged)
	}
}
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run 'TestGrantScriptLeavesANotNeededRightAlone|TestGrantScriptHeaderChecksWithTheProfile|TestNoAccessSectionUnderAProfile' -v`
Expected: compile failure, `in.Profile undefined`.

- [ ] Step 3: implement

Add to `GrantScriptInput`, before `Tool`:

```go
	// Profile is the collection profile the script is for, "" for the whole
	// corpus. Callers pass the members as Scripts and the checks after
	// ProfileChecks, so a right the profile does not need is not granted.
	Profile string
```

In `BuildGrantScript`, change `if len(in.NoAccessDatabases) > 0 {` to:

```go
	if len(in.NoAccessDatabases) > 0 && (in.Profile == "" || anyDatabaseScoped(in.Scripts)) {
```

and add:

```go
// anyDatabaseScoped reports whether a script runs inside each database, which
// is the only reason to ask for access to one.
func anyDatabaseScoped(scripts []Script) bool {
	for _, s := range scripts {
		if s.LintError == "" && s.Scope == ScopeDatabase {
			return true
		}
	}
	return false
}
```

In `writeGrantHeader`, in the format string: change the line `        login      %s` to `        login      %s%s`, so the header without a profile is byte-identical to today's (the second `%s` receives either "" or `"\n        profile    space"`); replace the line `        sql-auditor check` with `        %s`; replace `    Every line should come back "ok". Nothing else needs to change.` with `    Every line should come back %s. Nothing else needs to change.`. Extend the argument list, in that order, after `commentSafe(in.Login)`:

```go
		profileLine, checkCommand, comeBack)
```

computed before the `fmt.Fprintf`:

```go
	profileLine, checkCommand, comeBack := "", "sql-auditor check", `"ok"`
	if in.Profile != "" {
		profileLine = "\n        profile    " + commentSafe(in.Profile)
		checkCommand += " --profile " + commentSafe(in.Profile)
		comeBack = `"ok" or "not needed"`
	}
```

In the `WHAT THE PROBE FOUND` switch, add `case StatusNotNeeded: mark = "not needed"`.

In `collect/collect.go`, `writeGrantScript` takes the scripts and checks it is given; change its `BuildGrantScript` call to add `Profile: o.Profile`, and change the call site in `Check` to pass `ProfileMembers(v.Scripts, o.Profile)` and `checks`.

- [ ] Step 4: run and watch them pass

Run: `go test ./collect -run 'TestGrantScript|TestNoAccessSectionUnderAProfile|TestErrorLog|TestMsdb|TestAgentRole|TestErroredCapabilities|TestEveryProbedCapabilityCanBeGranted' -v`
Expected: 16 top-level tests: the 13 existing grant tests matched by these patterns (8 `TestGrantScript*`, `TestErrorLogNeedsNoSeparateGrantBefore2022`, `TestMsdbGrantsRunInMsdbAndCreateTheUserFirst`, `TestAgentRoleCarriesItsCaveat`, `TestErroredCapabilitiesGrantNothing`, `TestEveryProbedCapabilityCanBeGranted`) and the 3 new ones, all PASS. List the names if the count differs.

- [ ] Step 5: break it

Copy `grants.go` aside. (a) Remove `&& (in.Profile == "" || anyDatabaseScoped(in.Scripts))`: the no-access test must fail on its first assertion. (b) Restore, then pass `"sql-auditor check"` instead of `checkCommand`: the header test must fail. (c) Restore, then remove the `StatusNotNeeded` case in the switch: `TestGrantScriptLeavesANotNeededRightAlone` must fail on "not needed". Restore, rerun Step 4.

- [ ] Step 6: full suite and commit

Run: `go test ./...`

```bash
git add collect
git commit -m "collect: write the grant script for the requested profile" -m "A grant script for a space run should ask for what a space run reads, which is the argument that gets a cautious client to run it. Rights the profile does not need are marked rather than granted, the header tells the DBA to check again with the same profile, and per-database access is requested only when a member runs inside each database."
```

### Task 11: the `check` listing under a profile

Files:
- Modify: `collect/collect.go` (`Check`: extract the listing into `printQueries`)
- Test: `collect/profile_test.go`

Interfaces:
- Consumes: `profileCounts` (Task 5), `captureStdout` (Task 7 test helper), `scriptNote`.
- Produces: `func printQueries(o Options, scripts []Script)`; `func printQueryLine(o Options, s Script)`.

- [ ] Step 1: write the failing test

Append to `collect/profile_test.go`:

```go
func TestPrintQueriesUnderAProfile(t *testing.T) {
	scripts := []Script{
		{Path: "70.schema/050.heaps.sql", Profiles: []string{"space"}},
		{Path: "70.schema/099.custom.sql", Profiles: []string{"space"}, LintError: "@timeout: missing"},
		{Path: "80.workload/010.wait-stats.sql"},
		{Path: "80.workload/099.other.sql", LintError: "GO batch separator"},
	}
	out := captureStdout(t, func() { printQueries(Options{Profile: "space"}, scripts) })
	for _, want := range []string{
		"Profile: space, 1 of 2 collectors\n",
		"Queries (2):\n",
		"  70.schema/050.heaps.sql",
		"  !! 70.schema/099.custom.sql",
		"Not in profile space: 1 collectors. Run check without --profile to list them.\n",
		"Lint failures outside profile space (1):\n",
		"  !! 80.workload/099.other.sql",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "80.workload/010.wait-stats.sql") {
		t.Errorf("a clean script outside the profile must not be listed:\n%s", out)
	}
	whole := captureStdout(t, func() { printQueries(Options{}, scripts) })
	if !strings.HasPrefix(whole, "Queries (4):\n") || strings.Contains(whole, "Profile:") {
		t.Errorf("without a profile the listing is today's:\n%s", whole)
	}
}
```

- [ ] Step 2: run and watch it fail

Run: `go test ./collect -run TestPrintQueriesUnderAProfile -v`
Expected: compile failure, `undefined: printQueries`.

- [ ] Step 3: implement

In `Check`, replace the block from `fmt.Printf("Queries (%d):\n", len(v.Scripts))` through the end of its `for` loop with `printQueries(o, v.Scripts)`, and add below `Check`:

```go
// printQueries writes the Queries block of check. Under a profile it lists the
// scripts that declare the profile, lint failures included, and gives the lint
// failures of the other scripts a block of their own, so that each heading
// counts the lines under it.
func printQueries(o Options, scripts []Script) {
	if o.Profile == "" {
		fmt.Printf("Queries (%d):\n", len(scripts))
		for _, s := range scripts {
			printQueryLine(o, s)
		}
		return
	}
	members, corpus := profileCounts(scripts, o.Profile)
	var declared, outsideLint []Script
	for _, s := range scripts {
		switch {
		case slices.Contains(s.Profiles, o.Profile):
			declared = append(declared, s)
		case s.LintError != "":
			outsideLint = append(outsideLint, s)
		}
	}
	fmt.Printf("Profile: %s, %d of %d collectors\n", o.Profile, members, corpus)
	fmt.Printf("Queries (%d):\n", len(declared))
	for _, s := range declared {
		printQueryLine(o, s)
	}
	fmt.Printf("Not in profile %s: %d collectors. Run check without --profile to list them.\n",
		o.Profile, corpus-members)
	if len(outsideLint) > 0 {
		fmt.Printf("Lint failures outside profile %s (%d):\n", o.Profile, len(outsideLint))
		for _, s := range outsideLint {
			printQueryLine(o, s)
		}
	}
}

func printQueryLine(o Options, s Script) {
	if s.LintError != "" {
		fmt.Printf("  !! %-42s %s\n", s.Path, s.LintError)
		return
	}
	fmt.Printf("  %-42s %s\n", s.Path, scriptNote(s, o.Flags))
}
```

- [ ] Step 4: run and watch it pass

Run: `go test ./collect -run TestPrintQueriesUnderAProfile -v`
Expected: 1 test, PASS.

- [ ] Step 5: break it

Copy `collect.go` aside. (a) Put the `s.LintError != ""` case first in the `switch`: the `!! 70.schema/099.custom.sql` line moves to the wrong block and the heading counts change; the test must fail. (b) Restore, then print `len(scripts)` in the profile heading: the test must fail. Restore, rerun Step 4.

- [ ] Step 6: full suite and commit

Run: `go test ./...`

```bash
git add collect
git commit -m "collect: list the profile's collectors in check" -m "Under a profile the listing is about the collectors that profile runs; the rest collapse into one line, while a lint failure anywhere in the corpus stays visible in a block of its own so a broken custom corpus cannot hide behind the profile."
```

### Task 12: the wizard

Files:
- Modify: `collect/verify.go` (new `PlannedCollectors`)
- Modify: `tui/state.go` (`State.Profile`, `keyOptions`, `canStart`, `writeGrantScript`, `runFolderFor`, new helpers)
- Modify: `tui/run.go` (`pressEvent.apply`, `applyState`)
- Modify: `tui/grants.go` (`writeGrants`)
- Modify: `tui/render.go` (`permissionBlock`, `renderOptions`, `deniedPermissions`, new `visibleOptions`)
- Test: `collect/profile_test.go`, `tui/state_test.go`, `tui/render_test.go`, `tui/grants_test.go` (call updates)

Interfaces:
- Consumes: `ProfileChecks`, `StatusNotNeeded` (Task 9), `ProfileMembers` (Task 4), `KnownProfiles` (Task 1), `RunFolderFor` with profile (Task 6), `GrantScriptInput.Profile` (Task 10).
- Produces: `func PlannedCollectors(v VerifyResult, profile string, flags map[string]bool) int`; `State.Profile string`; `func (s State) withProfile(profile string, o collect.Options) State`; `func (s State) nextProfile() string`; `func (s State) collectors() int`; `func visibleFlags(s State) []string`; `func visibleOptions(s State) []option`; `func writeGrants(v collect.VerifyResult, profile, outputDir, tool string, now time.Time) (string, error)`.

Ruling recorded here, for review: without a profile, `collectors()` keeps returning `Verify.Collectors` as today, computed at verification. The spec asks for a recount whenever the profile or a flag changes; that holds under a profile. Without one, today's behaviour, which does not recount on a flag toggle, is kept, because changing it would move every existing wizard test that seeds `Collectors: 47` without scripts and is outside this feature.

- [ ] Step 1: write the failing tests

Append to `collect/profile_test.go`:

```go
func TestPlannedCollectorsFollowsProfile(t *testing.T) {
	v := VerifyResult{Probed: true, Server: ServerInfo{Version: "16.0.4135.4"},
		Checks: []CapabilityCheck{{Name: "connect", Status: "ok"}, {Name: "agent_alerts", Status: "denied"}},
		Scripts: []Script{
			{Path: "a.sql", Profiles: []string{"space"}, Permissions: []string{"connect"}},
			{Path: "b.sql", Profiles: []string{"space"}, RequiresFlag: FlagEstimateCompression},
			{Path: "c.sql", Permissions: []string{"agent_alerts"}},
		}}
	if got := PlannedCollectors(v, "space", nil); got != 1 {
		t.Errorf("space without flags = %d, want 1", got)
	}
	if got := PlannedCollectors(v, "space", map[string]bool{FlagEstimateCompression: true}); got != 2 {
		t.Errorf("space with the flag = %d, want 2", got)
	}
	if got := PlannedCollectors(v, "", nil); got != 1 {
		t.Errorf("no profile = %d, want 1: c.sql is denied and b.sql gated", got)
	}
	v.Probed = false
	if got := PlannedCollectors(v, "space", nil); got != 0 {
		t.Errorf("unprobed = %d, want 0", got)
	}
}
```

Append to `tui/state_test.go` (it imports `os`, `path/filepath`, `strings`, `time`, `collect`):

```go
// spaceVerify is a probed instance whose corpus has two space members, one
// behind a flag, and two scripts outside the profile, one needing a right the
// login was refused.
func spaceVerify() collect.VerifyResult {
	return collect.VerifyResult{
		Probed:     true,
		Collectors: 47,
		Server: collect.ServerInfo{Name: `SQL01\PROD`, Version: "16.0.4135.4",
			Edition: "Standard Edition", Login: "AUDIT_RO"},
		Checks: []collect.CapabilityCheck{
			{Name: "connect", Status: "ok"},
			{Name: "view_server_state", Status: "ok"},
			{Name: "agent_alerts", Status: "denied", Impact: "alerts not collected"},
		},
		Scripts: []collect.Script{
			{Path: "70.schema/050.heaps.sql", Scope: collect.ScopeDatabase,
				Profiles: []string{"space"}, Permissions: []string{"connect", "view_server_state"}},
			{Path: "70.schema/041.compression-savings.sql", Scope: collect.ScopeDatabase,
				Profiles: []string{"space"}, RequiresFlag: collect.FlagEstimateCompression,
				Permissions: []string{"connect"}},
			{Path: "80.workload/021.query-store-detail.sql", Scope: collect.ScopeDatabase,
				RequiresFlag: collect.FlagQueryStoreDetail, Permissions: []string{"connect"}},
			{Path: "50.agent/030.alerts.sql", Permissions: []string{"agent_alerts"}},
		},
	}
}

func TestProfileKeyOffersOnlyProfilesWithAMember(t *testing.T) {
	s := State{Step: StepOptions, Verify: spaceVerify()}
	if got := s.nextProfile(); got != "space" {
		t.Errorf("from none: next = %q, want space", got)
	}
	s.Profile = "space"
	if got := s.nextProfile(); got != "" {
		t.Errorf("from space: next = %q, want none", got)
	}
	bare := State{Step: StepOptions, Verify: collect.VerifyResult{Probed: true,
		Scripts: []collect.Script{{Path: "80.workload/010.wait-stats.sql"}}}}
	if got := bare.nextProfile(); got != "" {
		t.Errorf("a profile with no member must not be offered, got %q", got)
	}
}

func TestChangingTheProfileReprobesTheCollisionAndClearsTheGrant(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 14, 9, 30, 0, 0, time.UTC)
	s, o := probedAt(dir, now, false)
	s.Verify = spaceVerify()
	s.GrantPath = "grants-for-the-whole-corpus.sql"
	zip := filepath.Join(dir, collect.RunFolderName(`SQL01\PROD`, "space", now)+".zip")
	if err := os.WriteFile(zip, []byte("this morning's space run"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := pressEvent{key: typed('p'), opts: o}.apply(s)
	if got.Profile != "space" {
		t.Fatalf("Profile = %q, want space", got.Profile)
	}
	if !strings.Contains(got.Collision, zip) {
		t.Errorf("Collision = %q, want it to name %q: the banner was probed for the full-run name", got.Collision, zip)
	}
	if got.GrantPath != "" || got.GrantError != nil {
		t.Errorf("the grant result of the previous profile survived: %q, %v", got.GrantPath, got.GrantError)
	}
	got.Keep = true
	back := pressEvent{key: typed('p'), opts: o}.apply(got)
	if back.Profile != "" || back.Collision != "" || back.Keep {
		t.Errorf("back to none: Profile %q, Collision %q, Keep %v", back.Profile, back.Collision, back.Keep)
	}
}

func TestChangingTheProfileTurnsOffHiddenFlags(t *testing.T) {
	s := State{Step: StepOptions, Verify: spaceVerify(), FlagIndex: 5,
		Flags: map[string]bool{collect.FlagQueryStoreDetail: true, collect.FlagEstimateCompression: true}}
	got := s.withProfile("space", collect.Options{Config: &collect.Config{OutputDir: t.TempDir()}})
	if got.Flags[collect.FlagQueryStoreDetail] {
		t.Error("a flag with no member in the profile must be turned off")
	}
	if !got.Flags[collect.FlagEstimateCompression] {
		t.Error("a flag with a member keeps its value")
	}
	if n := len(visibleFlags(got)); got.FlagIndex >= n {
		t.Errorf("FlagIndex = %d with %d visible rows", got.FlagIndex, n)
	}
	// Tab and space on a profile must not panic when few or no rows are shown.
	_ = got.Key(named(screen.KeyTab)).Key(named(screen.KeySpace))
}

func TestTheCountAndTheStartGateFollowTheProfile(t *testing.T) {
	s := State{Step: StepOptions, Verify: spaceVerify(), Profile: "space"}
	if got := s.collectors(); got != 1 {
		t.Errorf("collectors = %d, want 1", got)
	}
	if !s.canStart() {
		t.Error("one collector will run, so the run can start")
	}
	s.Flags = map[string]bool{collect.FlagEstimateCompression: true}
	if got := s.collectors(); got != 2 {
		t.Errorf("collectors with the flag = %d, want 2", got)
	}
}

func TestTheGrantKeyOnScreenThreeWritesTheProfileScript(t *testing.T) {
	dir := t.TempDir()
	s := State{Step: StepOptions, Verify: spaceVerify(), Profile: "space"}
	opts := collect.Options{Config: &collect.Config{OutputDir: dir}, Version: "0.23.0"}
	got := pressEvent{key: typed('g'), opts: opts}.apply(s)
	if got.GrantPath == "" || got.GrantError != nil {
		t.Fatalf("GrantPath = %q, GrantError = %v", got.GrantPath, got.GrantError)
	}
	b, err := os.ReadFile(got.GrantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--profile space") || !strings.Contains(string(b), "not needed") {
		t.Errorf("the script is not the profile's:\n%s", b)
	}
	if got.Step != StepOptions {
		t.Errorf("Step = %v, want StepOptions", got.Step)
	}
}
```

If `tui/state_test.go` does not import `screen`, add `"github.com/rudi-bruchez/sql-auditor/tui/screen"` (the file uses `screen.Key` already through `named`/`typed`; confirm).

Append to `tui/render_test.go`:

```go
func TestScreenTwoShowsARightTheProfileDoesNotNeed(t *testing.T) {
	s := State{Step: StepVerification, Verify: spaceVerify(), Profile: "space"}
	block := strings.Join(permissionBlock(s, 100), "\n")
	if !strings.Contains(block, "not needed") || strings.Contains(block, "alerts not collected") {
		t.Errorf("agent_alerts must read not needed, without its impact:\n%s", block)
	}
	s.Profile = ""
	if block := strings.Join(permissionBlock(s, 100), "\n"); !strings.Contains(block, "denied") {
		t.Errorf("without a profile the refusal is shown as today:\n%s", block)
	}
}

func TestTheFinalScreenIgnoresRefusalsTheProfileDoesNotNeed(t *testing.T) {
	if got := deniedPermissions(State{Verify: spaceVerify(), Profile: "space"}); got != 0 {
		t.Errorf("deniedPermissions under space = %d, want 0: the manifest will say COMPLETE", got)
	}
	if got := deniedPermissions(State{Verify: spaceVerify()}); got != 1 {
		t.Errorf("deniedPermissions without a profile = %d, want 1", got)
	}
}
```

- [ ] Step 2: run and watch them fail

Run: `go test ./collect -run TestPlannedCollectorsFollowsProfile -v; go test ./tui -run 'TestProfileKey|TestChangingTheProfile|TestTheCountAndTheStartGate|TestTheGrantKeyOnScreenThree|TestScreenTwoShowsARight|TestTheFinalScreenIgnoresRefusals' -v`
Expected: compile failures (`undefined: PlannedCollectors`, `s.nextProfile undefined`, `s.Profile undefined`).

- [ ] Step 3: implement `PlannedCollectors`

In `collect/verify.go`:

```go
// PlannedCollectors is VerifyResult.Collectors for another profile or another
// set of flags: planScripts over v.Scripts, with the denied capabilities of
// ProfileChecks(v.Checks, v.Scripts, profile) and the version of v.Server. It
// is zero when v.Probed is false. Like countCollectors, it counts scripts that
// would run at least once and does not look at which databases were selected.
func PlannedCollectors(v VerifyResult, profile string, flags map[string]bool) int {
	if !v.Probed {
		return 0
	}
	denied := DeniedCapabilities(ProfileChecks(v.Checks, v.Scripts, profile))
	delete(denied, "connect")
	return countCollectors(planScripts(v.Scripts, profile, denied, ParseVersion(v.Server.Version), flags))
}
```

- [ ] Step 4: implement the state

In `tui/state.go`, add `"sort"` to the imports, and to `State` under `// Screen 3.`:

```go
	// Profile is the collection profile chosen with [p], "" for the whole
	// corpus. It changes the plan, the visible options, the run folder name and
	// the grant script, and every one of those is recomputed when it changes.
	Profile string
```

Add:

```go
// profilesWithMembers returns the known profiles with at least one member in
// scripts, sorted. A profile with none is not offered: choosing it could only
// make [enter] do nothing without saying why.
func profilesWithMembers(scripts []collect.Script) []string {
	var out []string
	for name := range collect.KnownProfiles {
		if len(collect.ProfileMembers(scripts, name)) > 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// nextProfile is the profile [p] moves to: none, then each offered profile.
func (s State) nextProfile() string {
	cycle := append([]string{""}, profilesWithMembers(s.Verify.Scripts)...)
	for i, p := range cycle {
		if p == s.Profile {
			return cycle[(i+1)%len(cycle)]
		}
	}
	return ""
}

// visibleFlags is flagOrder narrowed to the flags that gate a member of the
// profile, or flagOrder itself without a profile.
func visibleFlags(s State) []string {
	if s.Profile == "" {
		return flagOrder
	}
	members := collect.ProfileMembers(s.Verify.Scripts, s.Profile)
	var out []string
	for _, f := range flagOrder {
		for _, m := range members {
			if m.RequiresFlag == f {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// withProfile is the impure half of [p]: it needs the output directory to
// probe the collision again, because the folder name carries the profile.
func (s State) withProfile(profile string, o collect.Options) State {
	s.Profile = profile
	visible := map[string]bool{}
	for _, f := range visibleFlags(s) {
		visible[f] = true
	}
	flags := make(map[string]bool, len(s.Flags))
	for k, v := range s.Flags {
		if visible[k] {
			flags[k] = v
		}
	}
	s.Flags = flags
	if s.FlagIndex >= len(visibleFlags(s)) {
		s.FlagIndex = 0
	}
	before := s.Collision
	s.Collision = collisionFor(s, o)
	if s.Collision != before {
		s.Keep = false
	}
	// The script on screen was written for the previous profile.
	s.GrantPath, s.GrantError = "", nil
	return s
}

// collectors is the figure screen 3 shows and canStart reads. Without a
// profile it is the count made at verification, as before.
func (s State) collectors() int {
	if s.Profile == "" {
		return s.Verify.Collectors
	}
	return collect.PlannedCollectors(s.Verify, s.Profile, s.Flags)
}
```

In `keyOptions`, replace the `KeySpace` and `KeyTab` cases with:

```go
	case k.Named == screen.KeySpace:
		if visible := visibleFlags(s); s.FlagIndex < len(visible) {
			s.Flags = toggleFlag(s.Flags, visible[s.FlagIndex])
		}
	case k.Named == screen.KeyTab:
		if visible := visibleFlags(s); len(visible) > 0 {
			s.FlagIndex = (s.FlagIndex + 1) % len(visible)
		}
```

Change `canStart` to `return s.Verify.Probed && s.collectors() > 0`. Change `writeGrantScript` to call `writeGrants(s.Verify, s.Profile, outputDir, tool, now)`. Change `runFolderFor` to pass `s.Profile` instead of the `""` Task 6 left.

- [ ] Step 5: implement the events, the grant writer and the screens

In `tui/run.go`, `pressEvent.apply`:

```go
func (e pressEvent) apply(s State) State {
	if e.key.Rune == 'g' && (s.Step == StepVerification || s.Step == StepOptions) {
		return s.writeGrantScript(e.opts.Config.OutputDir, e.opts.Version, time.Now())
	}
	if e.key.Rune == 'p' && s.Step == StepOptions {
		return s.withProfile(s.nextProfile(), e.opts)
	}
	return s.Key(e.key)
}
```

In `applyState`, add `o.Profile = s.Profile` beside `o.Keep = s.Keep`.

In `tui/grants.go`, give `writeGrants` the `profile string` parameter after `v`, and build the input with:

```go
		Edition: v.Server.Edition, Checks: collect.ProfileChecks(v.Checks, v.Scripts, profile),
		Scripts: collect.ProfileMembers(v.Scripts, profile), Profile: profile,
```

Update the calls in `tui/grants_test.go` by inserting `""` after the first argument.

In `tui/render.go`:

- `permissionBlock`: build `byName` from `collect.ProfileChecks(s.Verify.Checks, s.Verify.Scripts, s.Profile)`, and before `body = append(body, fieldPad+status(check.Status)+c.Name)` add:

```go
		if check.Status == collect.StatusNotNeeded {
			body = append(body, fieldPad+status("not needed")+c.Name)
			ok++
			continue
		}
```

- `deniedPermissions`: range over `collect.ProfileChecks(s.Verify.Checks, s.Verify.Scripts, s.Profile)`.
- add:

```go
// visibleOptions is options narrowed to the rows visibleFlags keeps, in the
// same order, so FlagIndex points at the row it names.
func visibleOptions(s State) []option {
	keep := map[string]bool{}
	for _, f := range visibleFlags(s) {
		keep[f] = true
	}
	var out []option
	for _, o := range options(s) {
		if keep[o.flag] {
			out = append(out, o)
		}
	}
	return out
}
```

- `renderOptions`: replace the `Always collected` sentence by:

```go
	if s.Profile == "" {
		out = append(out, screen.Wrap(fmt.Sprintf("Always collected: %d collectors on this instance, computed from "+
			"the resolved plan. No statement text, no source code, no execution plans. Query Store configuration, "+
			"top queries and forced plans are included.", s.collectors()), width, pad)...)
	} else {
		out = append(out, screen.Wrap(fmt.Sprintf("Profile %s: %d collectors will run on this instance, "+
			"computed from the resolved plan.", s.Profile, s.collectors()), width, pad)...)
	}
	out = append(out, "")
	profileDesc := "none, the whole corpus"
	if s.Profile != "" {
		profileDesc = s.Profile + ": " + collect.KnownProfiles[s.Profile].Description
	}
	out = append(out, hang(pad+"Profile [p]", 29, profileDesc, width)...)
```

  replace the `Additional.` sentence by `"Additional. The first eight widen what the archive discloses; the last two only cost time."` without a profile and `"Additional. Only the options this profile can use are shown."` with one (Task 13 adds the tenth option; until then the unprofiled sentence still says "the last one", so keep today's sentence in this task and let Task 13 change it); iterate `visibleOptions(s)` instead of `options(s)` in the checkbox loop; before the `if !s.canStart()` block, add when `s.Profile != ""`:

```go
		out = append(out, fieldPad+"[g] write the T-SQL for this profile")
		switch {
		case s.GrantError != nil:
			out = append(out, screen.Wrap("could not write it: "+s.GrantError.Error(), width, fieldPad)...)
		case s.GrantPath != "":
			out = append(out, fieldPad+"written to:", fieldPad+"  "+s.GrantPath)
		}
		out = append(out, "")
```

  and change the keys line to `pad+"[tab] next   [space] toggle   [p] profile   "+start+"   [b] back   [q] quit"`.

- [ ] Step 6: run and watch them pass

Run: `go test ./collect -run TestPlannedCollectorsFollowsProfile -v && go test ./tui -run 'TestProfileKey|TestChangingTheProfile|TestTheCountAndTheStartGate|TestTheGrantKeyOnScreenThree|TestScreenTwoShowsARight|TestTheFinalScreenIgnoresRefusals' -v`
Expected: 1 test in `collect`, 7 in `tui`, all PASS.

- [ ] Step 7: break it

Copy the four `tui` files aside. (a) Remove the `collisionFor` line from `withProfile`: the collision test must fail. (b) Restore, then remove the `GrantPath` reset: the same test must fail on its second assertion. (c) Restore, then make `nextProfile` cycle over every key of `KnownProfiles`: the offer test must fail on the bare corpus. (d) Restore, then make `deniedPermissions` read `s.Verify.Checks`: its test must fail. (e) Restore, then make `canStart` read `s.Verify.Collectors`: re-run with a state whose Verify has `Collectors: 0` and a profile with a member; write that check inline and report whether it bit. Restore, rerun Step 6, report the count.

- [ ] Step 8: full suite and commit

Run: `go test ./...`

```bash
git add collect tui
git commit -m "tui: choose a profile on screen three" -m "The wizard is the only way an operator without a command line reaches a profile. Choosing one probes the same-day collision again, since the folder name carries the profile, hides the options the profile cannot use, drops the grant script written for the previous choice, and reads the permission statuses through the profile everywhere the screens show them, so the final screen agrees with the manifest."
```

### Task 13: `--measure-page-density` and `70.schema/055.page-density.sql`

Files:
- Create: `queries/70.schema/055.page-density.sql` (extracted verbatim from the spec)
- Modify: `collect/collect.go` (flag constant beside `FlagEstimateCompression`; `m.Config` in `Run`)
- Modify: `collect/queryset.go` (`KnownFlags`)
- Modify: `cmd/sql-auditor/main.go` (`cliFlags`, `defineFlags`, the `Flags` map in `optionsFrom`, `usage`, the `--all` help and comment)
- Modify: `tui/state.go` (`flagOrder`), `tui/render.go` (`options`, the `Additional.` sentence)
- Regenerate: `testdata/corpus.txt`

Interfaces:
- Produces: `const FlagMeasurePageDensity = "measure_page_density"`; `KnownFlags["measure_page_density"] = "--measure-page-density"`.

- [ ] Step 1: extract the collector from the spec

Run exactly:

```bash
python3 - <<'EOF'
import re, pathlib
spec = pathlib.Path("docs/profiles-spec.md").read_text()
blocks = [b for b in re.findall(r"```sql\n(.*?)\n```", spec, re.S) if "-- @scope:" in b]
assert len(blocks) == 1, len(blocks)
pathlib.Path("queries/70.schema/055.page-density.sql").write_text(blocks[0] + "\n")
print("written", len(blocks[0].splitlines()), "lines")
EOF
grep -nE '^--\s*@' queries/70.schema/055.page-density.sql
```

Expected: about 150 lines, and exactly six directive lines: `@scope`, `@resultsets`, `@permissions`, `@timeout: 1800`, `@requires_flag: measure_page_density`, `@profiles: space`. Any other line opening with `-- @` is a defect in the spec: stop and report it.

- [ ] Step 2: watch the corpus refuse it

Run: `go test . -run TestEmbeddedCorpusIsValid -v`
Expected: 1 test, FAIL: the new path is not in `testdata/corpus.txt`, and its lint says `@requires_flag: unknown flag "measure_page_density"`.

- [ ] Step 3: declare the flag

In `collect/collect.go`, beside `FlagEstimateCompression`:

```go
// FlagMeasurePageDensity turns on 70.schema/055.page-density.sql. Off for
// cost, not for disclosure: SAMPLED brings 8 to 12 % of every large index
// partition into the buffer pool, LOB pages included, and all of a small one.
const FlagMeasurePageDensity = "measure_page_density"
```

In `KnownFlags` (`collect/queryset.go`): `"measure_page_density": "--measure-page-density",`.

In `Run`, in the `m.Config` literal, add `"measure_page_density": fmt.Sprint(o.Flags[FlagMeasurePageDensity]),` beside `plan_cache_plans`.

In `cmd/sql-auditor/main.go`: add `measurePageDensity` to the `estimateCompression bool` line of `cliFlags`; after the `--estimate-compression` flag definition add:

```go
	// Off by default for cost, like --estimate-compression: the density scan
	// reads pages, not metadata, and on a large database that is tens of
	// gigabytes pulled into the buffer pool.
	fs.BoolVar(&c.measurePageDensity, "measure-page-density", false,
		"also measure how full the pages of the largest index partitions are: "+
			"this reads 8 to 12 % of every large partition into the buffer pool, LOB included, and all of a small one")
```

In the `Flags` map of `optionsFrom`: `collect.FlagMeasurePageDensity: c.all || c.measurePageDensity,`. Change the `--all` help to `"turn on every optional collector at once, including the ones off by default for disclosure and the ones off for cost"` and its comment from "nine" and "one is a cost decision" to "ten" and "two are cost decisions". In `usage()`, change the `--all` entry to say "all ten options below at once: the eight that are off for disclosure and the two that are off for cost" and "still records the ten individually", and add after the `--estimate-compression` entry:

```
  --measure-page-density      also measure how full the pages of the 50 largest
                              index partitions are, which says what a rebuild
                              would give back. Off for cost: SAMPLED reads 8 to
                              12 % of every large partition into the buffer pool,
                              LOB pages included, and all of a small one.
```

In `tui/state.go`, append `collect.FlagMeasurePageDensity` to `flagOrder` after `collect.FlagEstimateCompression`. In `tui/render.go`, `options`, append:

```go
		{collect.FlagMeasurePageDensity, "page density",
			"reads 8 to 12 % of every large index partition into the buffer pool, LOB included, and all of a small one", false},
```

and change the unprofiled `Additional.` sentence to `"Additional. The first eight widen what the archive discloses; the last two only cost time."`.

- [ ] Step 4: regenerate the inventory and run the tests

Run: `go test . -run TestEmbeddedCorpusIsValid -update -v && git diff testdata/corpus.txt && go test . -run 'TestEmbeddedCorpusIsValid|TestEveryKnownProfileHasACollector' -v && go test ./cmd/sql-auditor -run 'TestAllTurnsOnEveryOptIn|TestWithoutAllEveryOptInIsOff' -v && go test ./tui -run TestTheWizardOffersEveryOptInTheCommandLineHas -v`
Expected: the diff adds exactly `70.schema/055.page-density.sql @profiles: space`; then 2, 2 and 1 tests, all PASS. `TestAllTurnsOnEveryOptIn` and the wizard parity test compare against `KnownFlags`, so they prove the flag is wired everywhere without a number written down.

- [ ] Step 5: break it

(a) Remove the `Flags` map entry in `optionsFrom`: `TestAllTurnsOnEveryOptIn` must fail naming `measure_page_density`. (b) Restore, then remove it from `flagOrder`: the wizard parity test must fail. (c) Restore, then remove the `options` row: the same test must fail on the missing description. Restore, rerun Step 4 without `-update`.

- [ ] Step 6: run the collector against SQL Server, in one shell invocation

This fixture is the one the spec's verification used. Run it as one command:

```bash
set -e
go build -o /tmp/profiles-t13/sql-auditor ./cmd/sql-auditor
mkdir -p /tmp/profiles-t13/q/70.schema /tmp/profiles-t13/out
grep -vE '^-- @(requires_flag|profiles):' queries/70.schema/055.page-density.sql > /tmp/profiles-t13/q/70.schema/055.page-density.sql
Q() { podman exec -i sql2025 bash -c '/opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P "$MSSQL_SA_PASSWORD" -C -b -h -1 -W -i /dev/stdin'; }
Q <<'SQL'
CREATE DATABASE review_t13;
GO
USE review_t13;
SET NOCOUNT ON;
CREATE PARTITION FUNCTION pf(int) AS RANGE RIGHT FOR VALUES (2000, 4000);
CREATE PARTITION SCHEME ps AS PARTITION pf ALL TO ([PRIMARY]);
CREATE TABLE dbo.parted (id int NOT NULL, pad char(2000) NOT NULL) ON ps(id);
CREATE CLUSTERED INDEX cx ON dbo.parted(id) ON ps(id);
INSERT dbo.parted SELECT value, 'x' FROM GENERATE_SERIES(0, 5999);
CREATE TABLE dbo.withlob (id int NOT NULL PRIMARY KEY, pad char(2000) NOT NULL, body varchar(max) NOT NULL);
EXEC sp_tableoption 'dbo.withlob', 'large value types out of row', 1;
INSERT dbo.withlob SELECT value, 'x', REPLICATE(CAST('b' AS varchar(max)), 6000) FROM GENERATE_SERIES(1, 3000);
CREATE TABLE dbo.vbase (id int NOT NULL PRIMARY KEY, pad char(2000) NOT NULL);
INSERT dbo.vbase SELECT value, 'x' FROM GENERATE_SERIES(1, 6000);
GO
SET QUOTED_IDENTIFIER ON;
SET ANSI_NULLS ON;
GO
USE review_t13;
GO
CREATE VIEW dbo.v_idx WITH SCHEMABINDING AS SELECT id, pad FROM dbo.vbase;
GO
SET QUOTED_IDENTIFIER ON;
SET ANSI_NULLS ON;
USE review_t13;
CREATE UNIQUE CLUSTERED INDEX cx ON dbo.v_idx(id);
GO
SQL
: > /tmp/profiles-t13/empty.env
podman exec sql2025 printenv MSSQL_SA_PASSWORD | SQL_SERVER=localhost,11533 SQL_USER=sa SQL_TRUST_SERVER_CERTIFICATE=true DB_INCLUDE=review_t13 OUTPUT_DIR=/tmp/profiles-t13/out \
  /tmp/profiles-t13/sql-auditor collect --env /tmp/profiles-t13/empty.env --password-stdin --queries-dir /tmp/profiles-t13/q 2>&1 | grep 'result(s)'
python3 -c "
import json,glob; j=json.load(open(glob.glob('/tmp/profiles-t13/out/*/70.schema/review_t13/055.page-density.json')[0]))
print(j['counts'], j['errors']); print(sorted({(i['table'], i['index_name']) for i in j['indexes']}))"
echo "ALTER DATABASE review_t13 SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE review_t13; SELECT COUNT(*) FROM sys.databases WHERE name = N'review_t13';" | Q
```

Expected: `1 result(s), 0 skipped, 0 error(s)`; `{'eligible_partitions': 6, 'indexes_covered': 4}` and `{'indexes': 0}`; the index list includes `dbo.v_idx`; the last line prints `0`. If the counts differ, report the numbers; do not change the SQL to match.

- [ ] Step 7: full suite and commit

Run: `go test ./...`

```bash
git add queries/70.schema/055.page-density.sql testdata/corpus.txt collect cmd tui
git commit -m "queries: measure page density behind --measure-page-density" -m "Whether a rebuild gives space back depends on how full the pages are, and the corpus only read logical fragmentation, which measures page order. The collector samples the largest index partitions and is opt-in for cost: SAMPLED reads whole extents, so it brings 8 to 12 percent of every large partition into the buffer pool, LOB pages included, and all of a small one."
```

### Task 14: the table listing of `010.objects` and `060.columns`

Files:
- Modify: `queries/70.schema/010.objects.sql`, `queries/70.schema/060.columns.sql`
- Test: `queries_test.go` (new guard test)

Interfaces:
- Consumes: nothing from earlier tasks.
- Produces: root field `listing_cap_by_size` in both documents.

- [ ] Step 1: write the failing guard test

Append to `queries_test.go`:

```go
// The two collectors must choose the same tables, or the archive lists a table
// whose columns are missing. The selection is written three times, once in
// 010.objects and twice in 060.columns, and this keeps the copies identical.
func TestObjectsAndColumnsSelectTheSameTables(t *testing.T) {
	read := func(name string) string {
		b, err := sqlauditor.Queries.ReadFile("queries/70.schema/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	re := regexp.MustCompile(`(?s)SELECT by_rows\.object_id.*?AS by_size`)
	var copies []string
	for _, name := range []string{"010.objects.sql", "060.columns.sql"} {
		for _, m := range re.FindAllString(read(name), -1) {
			copies = append(copies, strings.Join(strings.Fields(m), " "))
		}
	}
	if len(copies) != 3 {
		t.Fatalf("found %d copies of the table selection, want 3 (one in 010.objects, two in 060.columns)", len(copies))
	}
	for i := 1; i < len(copies); i++ {
		if copies[i] != copies[0] {
			t.Errorf("copy %d differs from copy 0:\n%s\n%s", i, copies[i], copies[0])
		}
	}
}
```

Add `"regexp"` to the imports of `queries_test.go` if absent. If `sqlauditor.Queries` is not an `embed.FS` with `ReadFile`, use `fs.ReadFile(sqlauditor.Queries, ...)` and say so.

- [ ] Step 2: run and watch it fail

Run: `go test . -run TestObjectsAndColumnsSelectTheSameTables -v`
Expected: 1 test, FAIL, `found 0 copies`.

- [ ] Step 3: apply the change

The fragment is the one in the spec, section "Change to `70.schema/010.objects.sql` and `70.schema/060.columns.sql`". Apply it with this script, which asserts every anchor it replaces exists exactly as many times as expected:

```bash
python3 - <<'EOF'
import re, pathlib
spec = pathlib.Path("docs/profiles-spec.md").read_text()
frag = [b for b in re.findall(r"```sql\n(.*?)\n```", spec, re.S) if "SELECT by_rows.object_id" in b]
assert len(frag) == 1, len(frag)
frag = frag[0]
def sub(t, old, new, n):
    assert t.count(old) == n, (old[:70], t.count(old)); return t.replace(old, new)
d = pathlib.Path("queries/70.schema")
c = (d / "060.columns.sql").read_text()
old_cte = """    SELECT TOP (200) t.object_id
    FROM sys.tables AS t
    CROSS APPLY (SELECT SUM(p.row_count) AS row_count
                 FROM sys.dm_db_partition_stats AS p
                 WHERE p.object_id = t.object_id AND p.index_id IN (0, 1)) AS ps
    WHERE t.is_ms_shipped = 0
    ORDER BY ps.row_count DESC, t.object_id"""
c = sub(c, old_cte, frag, 2)
cap = "       200                                                        AS [listing_cap],"
c = sub(c, cap, cap + "\n       50                                                         AS [listing_cap_by_size],", 1)
(d / "060.columns.sql").write_text(c)
o = (d / "010.objects.sql").read_text()
o = sub(o, "    SELECT TOP (200)\n           SCHEMA_NAME(t.schema_id) + '.' + t.name,", "    SELECT\n           SCHEMA_NAME(t.schema_id) + '.' + t.name,", 1)
inner = "\n".join("          " + l[4:] if l.startswith("    ") else l for l in frag.splitlines())
o = sub(o, "    WHERE t.is_ms_shipped = 0\n    /* object_id is the tie-break", "    WHERE t.is_ms_shipped = 0\n      AND t.object_id IN (\n" + inner + ")\n    /* object_id is the tie-break", 1)
o = sub(o, cap, cap + "\n       50                                                         AS [listing_cap_by_size],", 1)
(d / "010.objects.sql").write_text(o)
print("applied")
EOF
```

Then extend the comment above the `ORDER BY` in `010.objects.sql` with two sentences: the list is the union of the 200 tables with the most rows and the 50 with the most reserved pages, so a large LOB table with few rows is kept; the `ORDER BY` no longer decides membership. Add the same two sentences above the first `sized` CTE of `060.columns.sql`.

- [ ] Step 4: run the tests

Run: `go test . -run 'TestObjectsAndColumnsSelectTheSameTables|TestEmbeddedCorpusIsValid' -v`
Expected: 2 tests, PASS.

- [ ] Step 5: break it

Copy `060.columns.sql` aside and change `TOP (50)` to `TOP (51)` in its second CTE only: the guard test must fail. Restore.

- [ ] Step 6: measure against SQL Server, in one shell invocation

```bash
set -e
go build -o /tmp/profiles-t14/sql-auditor ./cmd/sql-auditor
for v in before after; do mkdir -p /tmp/profiles-t14/$v/70.schema /tmp/profiles-t14/out-$v; done
git show HEAD:queries/70.schema/010.objects.sql > /tmp/profiles-t14/before/70.schema/010.objects.sql
git show HEAD:queries/70.schema/060.columns.sql > /tmp/profiles-t14/before/70.schema/060.columns.sql
cp queries/70.schema/010.objects.sql queries/70.schema/060.columns.sql /tmp/profiles-t14/after/70.schema/
Q() { podman exec -i sql2025 bash -c '/opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P "$MSSQL_SA_PASSWORD" -C -b -h -1 -W -i /dev/stdin'; }
{ echo "CREATE DATABASE review_t14;"; echo "GO"; echo "USE review_t14;"; echo "SET NOCOUNT ON;"
  for i in $(seq 1 201); do echo "CREATE TABLE dbo.small_$i (id int NOT NULL PRIMARY KEY); INSERT dbo.small_$i VALUES (1),(2);"; done
  echo "CREATE TABLE dbo.dense (id int NOT NULL PRIMARY KEY, pad char(400) NOT NULL); INSERT dbo.dense SELECT value, 'x' FROM GENERATE_SERIES(1, 50000);"
  echo "CREATE TABLE dbo.docs (id int NOT NULL PRIMARY KEY, body varbinary(max) NOT NULL); INSERT dbo.docs VALUES (1, CAST(REPLICATE(CAST('a' AS varchar(max)), 30000000) AS varbinary(max)));"
  echo "GO"; } | Q > /dev/null
: > /tmp/profiles-t14/empty.env
for v in before after; do
  podman exec sql2025 printenv MSSQL_SA_PASSWORD | SQL_SERVER=localhost,11533 SQL_USER=sa SQL_TRUST_SERVER_CERTIFICATE=true DB_INCLUDE=review_t14 OUTPUT_DIR=/tmp/profiles-t14/out-$v \
    /tmp/profiles-t14/sql-auditor collect --env /tmp/profiles-t14/empty.env --password-stdin --queries-dir /tmp/profiles-t14/$v 2>&1 | grep 'result(s)'
  python3 -c "
import json,glob; d=glob.glob('/tmp/profiles-t14/out-$v/*/70.schema/review_t14')[0]
o=json.load(open(d+'/010.objects.json')); c=json.load(open(d+'/060.columns.json'))
print('$v', 'tables', len(o['tables']), 'docs', any(x['table']=='dbo.docs' for x in o['tables']), 'docs columns', [x['column'] for x in c['columns'] if x['table']=='dbo.docs'], 'covered', c.get('tables_covered'))"
done
echo "ALTER DATABASE review_t14 SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE review_t14; SELECT COUNT(*) FROM sys.databases WHERE name = N'review_t14';" | Q
```

Expected: `before tables 200 docs False docs columns [] covered 200`, then `after tables 201 docs True docs columns ['id', 'body'] covered 201`, then `0`. Report the four numbers.

- [ ] Step 7: full suite and commit

Run: `go test ./...`

```bash
git add queries/70.schema/010.objects.sql queries/70.schema/060.columns.sql queries_test.go
git commit -m "queries: keep the largest tables in the object and column listings" -m "Both listings kept the 200 tables with the most rows, so a table of documents holding few rows and a large LOB footprint fell out of them on any database with more tables than that, and it is exactly the table a space question is about. They now keep the union of the 200 with the most rows and the 50 with the most reserved pages, and a test keeps the three copies of that selection identical."
```

### Task 15: documentation and changelog

Files:
- Modify: `README.md`, `docs/dba-guide.md`, `CHANGELOG.md`

Interfaces:
- Consumes: the behaviour of Tasks 1 to 14, which must be committed first.

The prose below is to be committed as written, in the places named. No bold, no em dash, no client identifier.

- [ ] Step 1: `README.md`

In the table under "Connection and output", after the `--keep` row, add:

```
| `--profile NAME` | collect only the collectors of a profile. The one profile is `space`. Refused beside `--all`. See [Collecting for one question](#collecting-for-one-question) |
```

In the table under "Collecting more than the default", change the `--all` row to say "turn on all ten options below at once: the eight off for disclosure and the two off for cost", and add after the `--estimate-compression` row:

```
| `--measure-page-density` | also measure how full the pages of the 50 largest index partitions are, which says what a rebuild would give back. Off for cost, not for disclosure: `SAMPLED` reads 8 to 12 % of every large partition into the buffer pool, LOB pages included, and all of a small one |
```

In "`--all` asks for the widest archive this tool can produce", change "eight of the nine collectors it turns on" to "eight of the ten collectors it turns on", and "no extra collectors beyond the nine" to "no extra collectors beyond the ten".

Add this section immediately before `## Passwords`:

```markdown
## Collecting for one question

`--profile space` runs only the collectors that answer one question: what makes
the databases on this instance larger than they need to be, and what could be
given back without buying disk. That is 21 of the 84 collectors: index usage and
size, compression, page fullness, files and volumes, the transaction log,
tempdb, and the Agent jobs and maintenance plans a scheduled shrink hides in.

A profile only removes collectors. It never adds one: the two collectors of the
space profile that are opt-in still need their option.

- `--measure-page-density` is the one a space question needs most, because it
  says whether a rebuild would give anything back. It reads pages, not
  metadata, so decide per instance.
- `--estimate-compression` gives the saving a compression pass would bring. It
  reads sampled data into tempdb.

What changes in the archive:

- the run folder and the archive are named `<server>-<date>-space`, so a full
  run and a space run of the same day do not replace each other;
- `MANIFEST.txt` says which profile produced it, and lists the collectors the
  profile left out in one line;
- a right the login was refused and that no collector of the profile reads is
  reported as `not needed` and does not make the coverage incomplete.

`check --profile space` lists the profile's collectors, and
`check --profile space --grant-script grants.sql` asks only for what a space run
reads. In the wizard, `[p]` on the third screen chooses the profile.

`--profile` is refused beside `--all`, which asks for the opposite, and when it
would collect nothing: an option with no collector in the profile, or a
`--queries-dir` corpus exported before profiles existed, which declares none.
```

- [ ] Step 2: `docs/dba-guide.md`

Rename `### Nine files are opt-in` to `### Ten files are opt-in`. In its table, add after the `70.schema/041.compression-savings.sql` row:

```
| `70.schema/055.page-density.sql` | `--measure-page-density` |
```

Replace the sentence that starts "Eight of the nine change what kind of data ends up in the archive" and its continuation with:

```markdown
Eight of the ten change what kind of data ends up in the archive and have
sections of their own below. `--estimate-compression` and
`--measure-page-density` are opt-in for cost rather than for disclosure.
```

In "What the default run costs a large instance", replace the text of the `Sampled page reads on heaps` row's last cell with:

```
`sys.dm_db_index_physical_stats(..., 'SAMPLED')` on the 50 largest heaps **in every collected database**. SAMPLED works allocation unit by allocation unit: a unit of 10,000 pages or more brings 8 to 12 % of its pages into the buffer pool, because each sample reads a whole extent, and a smaller one is read in full. On a 500 GB heap that is in the order of 40 to 60 GB of reads, not the 1 % the name suggests.
```

The bold inside that cell is already there today; leave it as it is and add none.

After the paragraph about `--estimate-compression` at the end of that section, add:

```markdown
`--measure-page-density` is opt-in for the same reason as the heap scan above is
costly: it runs the same kind of sampled read on the 50 largest index partitions
of each database, LOB pages included. It is what tells a space question whether
a rebuild would give anything back, and it is the operator's decision per
instance.
```

Add this section immediately before `## What is in the archive`:

```markdown
## Collecting for one question

`--profile space` is for an engagement that asks one question: how to make the
databases of this instance smaller. It runs 21 collectors instead of 84, reads
nothing the full run does not read, and changes nothing in what the manifest
promises.

What to approve differs in three ways.

- The archive is smaller and says so: `MANIFEST.txt` names the profile and
  counts the collectors it left out.
- The rights to grant are fewer. `check --profile space --grant-script
  grants.sql` writes only what a space run needs.
- A right refused to the login that no collector of the profile reads is
  printed `not needed` by `check`, marked `not needed` in the grant script, and
  listed in `MANIFEST.txt` under "Not needed by profile space". It does not make
  the coverage incomplete. A probe that got no answer is never reported that
  way: it still means the instance was unreachable, and `check` still exits 1.

The two opt-in collectors of the profile, `--estimate-compression` and
`--measure-page-density`, are decided as they are for a full run, and their
cost is described under [What the default run costs a large
instance](#what-the-default-run-costs-a-large-instance).
```

- [ ] Step 3: `CHANGELOG.md`

Insert immediately before `## [0.22.0] - 2026-09-06`:

```markdown
## [Unreleased]

### Added

- `--profile space` on `check`, `collect` and the wizard: runs only the 21
  collectors that answer what makes the databases of an instance larger than
  they need to be. The archive names the profile, the run folder carries it,
  and rights the profile does not need are reported `not needed`.
- `70.schema/055.page-density.sql`, behind `--measure-page-density`: page
  fullness of the 50 largest rowstore index partitions, indexed views included.
  84 collectors.

### Changed

- `70.schema/010.objects.sql` and `70.schema/060.columns.sql` list the union of
  the 200 tables with the most rows and the 50 with the most reserved pages, so
  a large LOB table with few rows is no longer left out.
- The replication widening brings the distribution database into a narrowed run
  only when a collector that will run reads it. On an instance where the
  replication collectors are gated off, it is no longer listed as covered.
- `--all` turns on ten options.

### Fixed

- `docs/dba-guide.md` stated that the heap scan reads about 1 % of the pages.
  Measured, it brings 8 to 12 % of a large heap into the buffer pool and all of
  a small one.
```

- [ ] Step 4: check the prose

Run: `grep -nP '[\x{2013}\x{2014}]' README.md docs/dba-guide.md CHANGELOG.md | grep -n 'profile\|page-density\|not needed\|Ten files\|Unreleased'` and `git diff | grep -nE '^\+.*\*\*' | grep -v 'in every collected database'`.
Expected: no output from either. Any output is a new em dash or new bold in the added text: remove it.

- [ ] Step 5: commit

Run: `go test ./...`

```bash
git add README.md docs/dba-guide.md CHANGELOG.md
git commit -m "docs: document collection profiles and the page density option" -m "An operator and the DBA who approves the run both need to know what a profile removes and what it does not, what not needed means in check and in the manifest, and what the new opt-in costs. The heap scan cost stated in the DBA guide was wrong by a factor of about ten, measured while designing the page density collector, and is corrected in the same change."
```

### Task 16: end-to-end verification against SQL Server

Files:
- None changed. This task reads what the finished feature produces.

- [ ] Step 1: the static checks

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: `gofmt -l` prints nothing; `go vet` and `go test` pass.

- [ ] Step 2: check and collect under the profile, in one shell invocation

```bash
set -e
W=/tmp/profiles-t16; rm -rf $W; mkdir -p $W/out
go build -o $W/sql-auditor ./cmd/sql-auditor
Q() { podman exec -i sql2025 bash -c '/opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P "$MSSQL_SA_PASSWORD" -C -b -h -1 -W -i /dev/stdin'; }
Q <<'SQL' > /dev/null
CREATE DATABASE review_t16;
GO
USE review_t16;
SET NOCOUNT ON;
CREATE TABLE dbo.dense (id int NOT NULL PRIMARY KEY, pad char(400) NOT NULL, k int NOT NULL);
INSERT dbo.dense SELECT value, 'x', value % 1000 FROM GENERATE_SERIES(1, 100000);
CREATE INDEX ix_dense_k ON dbo.dense (k);
DELETE FROM dbo.dense WHERE id % 2 = 0;
GO
SQL
: > $W/empty.env
P() { podman exec sql2025 printenv MSSQL_SA_PASSWORD | SQL_SERVER=localhost,11533 SQL_USER=sa SQL_TRUST_SERVER_CERTIFICATE=true DB_INCLUDE=review_t16 OUTPUT_DIR=$W/out "$W/sql-auditor" "$@" --env $W/empty.env --password-stdin; }
P check --profile space --grant-script $W/grants.sql > $W/check.txt 2>&1 || true
P collect --profile space --measure-page-density --estimate-compression > $W/collect.txt 2>&1
P collect --profile space --all > $W/refused.txt 2>&1 && echo "UNEXPECTED: --all was accepted" || echo "refused with exit $?"
ls $W/out
sed -n '1,20p' $W/check.txt
grep -n 'Profile\|Not in profile' $W/check.txt
grep -n 'profile' $W/grants.sql | head -5
D=$(ls -d $W/out/*-space | head -1)
grep -n '^Profile\|collectors outside profile\|Coverage' -A1 $D/MANIFEST.txt | head -12
python3 -c "import json; j=json.load(open('$D/_run.json')); print(j['profile']); print(len(j['skipped_scripts']), 'skipped'); print(sorted({r['script'] for r in j['results']})[:30])"
ls $D/70.schema/review_t16/
cat $W/refused.txt | tail -2
echo "ALTER DATABASE review_t16 SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE review_t16; SELECT COUNT(*) FROM sys.databases WHERE name = N'review_t16';" | Q
```

Expected, and report each:

- `ls $W/out` shows a folder and a zip whose names end in `-space`;
- `check.txt` starts with `Profile: space, 21 of 84 collectors`, then `Queries (21):`, then `Not in profile space: 63 collectors`;
- `grants.sql` names `for profile space` and `sql-auditor check --profile space` (with `sa`, it also says there is nothing to grant);
- `MANIFEST.txt` has `Profile      : space, 21 of the 84 collectors in the corpus belong to it` and one line `63 collectors outside profile space, each listed in _run.json`;
- `_run.json` has `{'name': 'space', 'members': 21, 'corpus': 84}`;
- the results include `70.schema/055.page-density.sql` and `70.schema/041.compression-savings.sql`, and no `80.workload/` collector;
- `review_t16` holds `055.page-density.json`, whose `dbo.dense` clustered row shows a `page_fullness_pct` near 50;
- the `--all` run is refused with exit 2 and the `cannot be combined` message;
- the last line prints `0`.

- [ ] Step 3: what this task cannot verify, and must say

`sa` is refused nothing, so `not_needed` never appears in this run. Verifying it live needs a login that is refused `agent_alerts` or `log_shipping`, and creating one is a permission change this plan does not make. Report that the live `not_needed` path is covered by the unit tests of Tasks 9, 10 and 12 only, and ask the owner whether to verify it with a login he creates.

The replication widening cannot be observed on this container, which has no publication. It is covered by the unit tests of Task 8 only; say so.

- [ ] Step 4: leave the container and the tree as found

Run: `podman exec sql2025 bash -c '/opt/mssql-tools18/bin/sqlcmd -S localhost -U sa -P "$MSSQL_SA_PASSWORD" -C -h -1 -W -Q "SET NOCOUNT ON; SELECT name FROM sys.databases WHERE name LIKE N'"'"'review%'"'"';"'` (expect no row) and `git status --porcelain -uall` (expect nothing). Nothing to commit in this task.

---

## Rulings taken while writing this plan

Each is a decision the spec did not settle to the letter, with what it costs if wrong.

1. Without a profile, the wizard's figure and start gate keep reading `Verify.Collectors` (Task 12). The spec's recount on every change holds under a profile. Cost if wrong: an unprofiled wizard keeps today's behaviour of not recounting after a flag toggle, which predates this feature.
2. The grant script omits the per-database section for a profile without database-scoped members only when a profile is given (Task 10). Without one, existing fixtures whose scripts carry no scope keep their section. Cost if wrong: none for `space`, which has database-scoped members.
3. `ProfileBlock.Members` is 0 without a profile (Task 5). Cost if wrong: a reader of `_run.json` must read `name` before `members`, which the spec's example already implies.
4. `055.page-density.sql` and the listing fragment are extracted from the spec by script rather than copied into this plan (Tasks 13 and 14). The spec's copies are the ones measured on SQL Server; a second copy here could drift. Cost if wrong: an implementer without the spec cannot do those two tasks, and the plan names the spec as required reading.
5. The live `not_needed` path is not verified against SQL Server (Task 16), because it needs a login this plan must not create.

## Spec coverage

| Spec section | Task |
| --- | --- |
| The directive | 1, 2 |
| Selecting the plan | 3 |
| Selecting the databases | 8 |
| The command line: four refusals, where they happen | 4, 7 |
| Permissions under a profile | 9 |
| The grant script | 10 |
| What the archive says: `_run.json`, `MANIFEST.txt` | 5 |
| The run folder | 6 |
| `check` | 9 (permissions), 11 (queries) |
| The wizard | 12 |
| The `space` profile: members | 2, 13 |
| New collector, flag, cost, unit, file | 13 |
| Change to `010.objects` and `060.columns` | 14 |
| Tests (root package, corpus inventory) | 2, 13, 14 |
| Documentation | 15 |
| Verification against a real instance | 13, 14, 16 |
