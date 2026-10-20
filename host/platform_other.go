//go:build !windows

package host

// WindowsPlatform refuses on every other system: Run stops at Check.
func WindowsPlatform(*Instance) Platform {
	return Platform{Check: func() error { return ErrNotWindows }}
}
