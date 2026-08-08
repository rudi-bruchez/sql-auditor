package collect

import (
	"fmt"
	"path"
	"strings"
	"time"
)

type DatabaseFolder struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
}

// reservedNames are Windows device names. They are reserved as the stem too,
// so "CON.reports" is just as illegal as "CON".
var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

const invalidChars = `<>:"/\|?*,`

func SafeFolderName(name string) string {
	var runes []rune
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(invalidChars, r) {
			runes = append(runes, '_')
			continue
		}
		runes = append(runes, r)
	}
	// Truncate by rune, not byte, so a multi-byte rune at the boundary is
	// never split into invalid UTF-8.
	if len(runes) > 100 {
		runes = runes[:100]
	}
	s := strings.TrimRight(string(runes), ". ")
	if s == "" {
		s = "_"
	}
	stem := s
	if i := strings.Index(s, "."); i >= 0 {
		stem = s[:i]
	}
	if reservedNames[strings.ToUpper(stem)] {
		s = stem + "_" + s[len(stem):]
	}
	return s
}

// ResolveDatabaseFolders assigns one folder per database, suffixing ~2, ~3 …
// when two distinct names sanitise to the same folder. Silently merging them
// would put two databases' results in one directory. Collisions are detected
// case-insensitively (keyed on the upper-cased folder name) because Windows
// directories are case-insensitive even when the SQL Server collation is
// case-sensitive; the original casing is still preserved in the emitted
// folder name.
func ResolveDatabaseFolders(names []string) []DatabaseFolder {
	taken := map[string]bool{}
	out := make([]DatabaseFolder, 0, len(names))
	for _, n := range names {
		base := SafeFolderName(n)
		f := base
		// Both the candidate and every generated suffix must be checked, or a
		// database genuinely named "a~2" collides with the suffix minted for
		// a database named "A".
		for i := 2; taken[strings.ToUpper(f)]; i++ {
			f = fmt.Sprintf("%s~%d", base, i)
		}
		taken[strings.ToUpper(f)] = true
		out = append(out, DatabaseFolder{Name: n, Folder: f})
	}
	return out
}

func RunFolderName(server string, t time.Time) string {
	return fmt.Sprintf("%s-%s", SafeFolderName(server), t.Format("2006-01-02"))
}

func ResultRelativePath(dir, base, dbFolder string) string {
	if dbFolder == "" {
		return path.Join(dir, base+".json")
	}
	return path.Join(dir, dbFolder, base+".json")
}
