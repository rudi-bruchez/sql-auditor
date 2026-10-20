// Package host reads what the Windows machine knows about a SQL Server
// instance and its surroundings: event logs, cluster state, extension logs and
// job output files, all for one window of time.
package host

import (
	"fmt"
	"time"
)

// DefaultDays is the window when --since is not given, the window the private
// reader and its topics were written against.
const DefaultDays = 90

// ParseSince returns the start of the window: local midnight of the date s,
// read as yyyy-mm-dd in now's location, or DefaultDays before today when s is
// empty. A date after today is refused, since it would read nothing.
func ParseSince(s string, now time.Time) (time.Time, error) {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	if s == "" {
		return today.AddDate(0, 0, -DefaultDays), nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, now.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("--since %q: expected a date as yyyy-mm-dd", s)
	}
	if t.After(today) {
		return time.Time{}, fmt.Errorf("--since %s is in the future", s)
	}
	return t, nil
}
