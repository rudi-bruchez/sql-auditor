package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// grantFileName is where [g] writes, relative to OUTPUT_DIR.
//
// check --grant-script demands an explicit path; the wizard's key has none to
// demand, and a double-clicked binary can have any working directory at all,
// C:\Windows\System32 included. So the destination is derived rather than
// asked for, and it is derived the same way the run folders are: through
// SafeFolderName, because every named instance carries a backslash and the
// date, because a second probe a week later is a different answer about a
// different instance state.
func grantFileName(instance string, t time.Time) string {
	return "grants-" + collect.SafeFolderName(instance) + "-" + t.Format("2006-01-02") + ".sql"
}

// freeName returns a path in dir that nothing occupies, suffixing -2, -3 …
// before the extension until it finds one.
//
// Never overwriting is the point. The file already sitting there may be the
// one a DBA is halfway through reviewing, or the one already sent to whoever
// approves grants, and this key exists to help that review rather than to
// silently replace its subject. The suffix goes before the extension so the
// result is still a .sql file to every editor that decides by extension.
func freeName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	candidate := filepath.Join(dir, name)
	for i := 2; ; i++ {
		_, err := os.Stat(candidate)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return candidate, nil
		case err != nil:
			// Not "taken": a directory that cannot be stat'ed at all is a
			// failure the caller must report rather than route around by
			// picking another name in the same unusable place.
			return "", err
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
	}
}

// writeGrants writes the T-SQL that grants exactly what the probe found
// missing, and returns the absolute path of the file it created.
//
// It writes; it never executes. That is the contract --grant-script already
// keeps, and the reason this wizard is allowed to touch permissions at all: a
// DBA reads the file, decides, and runs it themselves.
//
// The content comes from collect.BuildGrantScript, which is why VerifyResult
// carries Scripts — each section of the file lists the collectors that
// declared the permission it grants, so the reader sees what a line buys in
// terms of output rather than in terms of a permission name.
func writeGrants(v collect.VerifyResult, outputDir, tool string, now time.Time) (string, error) {
	// The same two refusals check makes, for the same reason: the login must
	// be the one the SERVER reports, since a Windows login arrives as
	// DOMAIN\user whatever was typed and can be reached through a group. A
	// script naming the configured string would grant to a principal nobody
	// is connecting with — worse than failing, because it looks like it
	// worked.
	if !v.Probed {
		return "", fmt.Errorf("the server probe failed, so the login and version are unknown: %w", v.ServerErr)
	}
	if strings.TrimSpace(v.Server.Login) == "" {
		return "", errors.New("the server did not report a login name for this connection")
	}
	body, _ := collect.BuildGrantScript(collect.GrantScriptInput{
		Login: v.Server.Login, Instance: v.Server.Name, Version: v.Server.Version,
		Edition: v.Server.Edition, Checks: v.Checks, Scripts: v.Scripts,
		NoAccessDatabases: v.NoAccess, Tool: tool,
	})
	// A file with nothing to grant is still written, exactly as check does:
	// "nothing is missing" is an answer worth having on paper when the
	// question comes back a week later.
	dir, err := filepath.Abs(outputDir)
	if err != nil {
		return "", err
	}
	path, err := freeName(dir, grantFileName(v.Server.Name, now))
	if err != nil {
		return "", err
	}
	// 0o600: the file names a login and everything it is not allowed to do,
	// on a machine the auditor does not own.
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
