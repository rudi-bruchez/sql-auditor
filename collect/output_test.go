package collect

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSafeFolderName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Northwind", "Northwind"},
		{`bad/name\here`, "bad_name_here"},
		{"trailing. ", "trailing"},
		{"a:b*c?d", "a_b_c_d"},
		{"CON", "CON_"},                 // Windows reserved device name
		{"CON.reports", "CON_.reports"}, // reserved as a stem, too
		{"com1", "com1_"},
	}
	for _, tc := range tests {
		if got := SafeFolderName(tc.in); got != tc.want {
			t.Errorf("SafeFolderName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'x'
	}
	if got := SafeFolderName(string(long)); len(got) != 100 {
		t.Errorf("long name truncated to %d chars, want 100", len(got))
	}
}

func TestSafeFolderNameTruncatesByRuneNotByte(t *testing.T) {
	// 99 ASCII chars followed by several multi-byte runes ('é' is 2 bytes in
	// UTF-8). A byte-based truncation at 100 bytes would split the rune at
	// the boundary and produce invalid UTF-8.
	var b strings.Builder
	for i := 0; i < 99; i++ {
		b.WriteByte('x')
	}
	for i := 0; i < 10; i++ {
		b.WriteRune('é')
	}
	got := SafeFolderName(b.String())
	if !utf8.ValidString(got) {
		t.Errorf("SafeFolderName truncated to invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 100 {
		t.Errorf("SafeFolderName truncated to %d runes, want 100", n)
	}
}

func TestResolveDatabaseFoldersDisambiguates(t *testing.T) {
	// Two distinct database names can sanitise to the same folder.
	got := ResolveDatabaseFolders([]string{"a/b", `a\b`, "other"})
	if got[0].Folder != "a_b" {
		t.Errorf("first folder = %q, want a_b", got[0].Folder)
	}
	if got[1].Folder != "a_b~2" {
		t.Errorf("second folder = %q, want a_b~2", got[1].Folder)
	}
	if got[2].Folder != "other" {
		t.Errorf("third folder = %q, want other", got[2].Folder)
	}
}

func TestResolveDatabaseFoldersDisambiguatesCaseInsensitively(t *testing.T) {
	// Windows directories are case-insensitive even when the SQL Server
	// collation is case-sensitive, so "Northwind", "northwind" and
	// "NORTHWIND" must all be treated as colliding.
	got := ResolveDatabaseFolders([]string{"Northwind", "northwind", "NORTHWIND"})
	if got[0].Folder != "Northwind" {
		t.Errorf("first folder = %q, want Northwind", got[0].Folder)
	}
	if got[1].Folder != "northwind~2" {
		t.Errorf("second folder = %q, want northwind~2", got[1].Folder)
	}
	if got[2].Folder != "NORTHWIND~3" {
		t.Errorf("third folder = %q, want NORTHWIND~3", got[2].Folder)
	}
}

func TestResolveDatabaseFoldersSuffixItselfCanCollide(t *testing.T) {
	// The generated suffix "a~2" (minted for the second "a"-like name) must
	// itself be checked against a database literally named "a~2", or the two
	// fold to the same Windows directory.
	got := ResolveDatabaseFolders([]string{"a", "A", "a~2"})
	want := []string{"a", "A~2", "a~2~2"}
	for i, w := range want {
		if got[i].Folder != w {
			t.Errorf("folder[%d] = %q, want %q", i, got[i].Folder, w)
		}
	}
}

func TestResolveDatabaseFoldersNoFoldedDuplicates(t *testing.T) {
	// Regression guard: whatever the algorithm, no two emitted folders may
	// fold to the same name under Windows' case-insensitive comparison.
	names := []string{"Northwind", "northwind", "NORTHWIND", "a", "A", "a~2"}
	got := ResolveDatabaseFolders(names)
	seen := map[string]DatabaseFolder{}
	for _, df := range got {
		key := strings.ToUpper(df.Folder)
		if prev, ok := seen[key]; ok {
			t.Errorf("folders %q (for db %q) and %q (for db %q) fold to the same name %q",
				prev.Folder, prev.Name, df.Folder, df.Name, key)
		}
		seen[key] = df
	}
}

func TestRunFolderName(t *testing.T) {
	ts := time.Date(2026, 8, 8, 9, 5, 0, 0, time.UTC)
	if got := RunFolderName(`SRV01\INST`, ts); got != "SRV01_INST-2026-08-08" {
		t.Errorf("RunFolderName = %q", got)
	}
}

func TestResultRelativePath(t *testing.T) {
	if got := ResultRelativePath("10.system", "010.properties", ""); got != "10.system/010.properties.json" {
		t.Errorf("instance path = %q", got)
	}
	if got := ResultRelativePath("20.databases", "020.properties", "Northwind"); got != "20.databases/Northwind/020.properties.json" {
		t.Errorf("database path = %q", got)
	}
}

func TestMarkWidenedTagsOnlyTheWidenedFolders(t *testing.T) {
	folders := ResolveDatabaseFolders([]string{"SALESDB", "DISTDB"})
	got := MarkWidened(folders, map[string]WidenedFor{
		"DISTDB": {
			Purpose: "replication",
			Reason:  "local distributor for 1 published database(s) in this selection",
		},
	})
	for _, f := range got {
		switch f.Name {
		case "DISTDB":
			// The purpose is what planUnits compares. Putting the sentence
			// here instead makes that comparison false for every collector,
			// and the database is then read by none of them.
			if f.WidenedPurpose != "replication" {
				t.Errorf("DISTDB WidenedPurpose = %q, want the purpose planUnits matches on", f.WidenedPurpose)
			}
			if !strings.Contains(f.RetentionReason, "local distributor") {
				t.Errorf("DISTDB RetentionReason = %q, want the human sentence", f.RetentionReason)
			}
		case "SALESDB":
			if f.WidenedPurpose != "" || f.RetentionReason != "" {
				t.Errorf("SALESDB must not be marked, got %q / %q", f.WidenedPurpose, f.RetentionReason)
			}
		}
	}
}
