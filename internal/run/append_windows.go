//go:build windows

package run

import "errors"

// appendWithLock is unimplemented on Windows. Cross-process locking on
// Windows uses LockFileEx, but fettle is unix-only for v0. If a Windows
// path is needed later, this is the place.
func appendWithLock(path string, line []byte) error {
	return errors.New("cross-process append locking is not supported on windows")
}
