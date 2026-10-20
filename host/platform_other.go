//go:build !windows

package host

import "os"

// WindowsPlatform refuses on every other system: Run stops at Check.
func WindowsPlatform(*Instance) Platform {
	return Platform{Check: func() error { return ErrNotWindows }}
}

// createExclusive creates path for writing, refusing an existing name.
func createExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
