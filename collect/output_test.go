package collect

import (
	"testing"
	"time"
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
