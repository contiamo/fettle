//go:build unix

package run

import (
	"os"

	"golang.org/x/sys/unix"
)

// appendWithLock appends one line of bytes to path under an exclusive
// flock(2) on the file's own file descriptor. The lock auto-releases
// when fd is closed (including on process crash). Other processes that
// don't call flock are unaffected — flock is advisory — so readers
// (the UI, tail -f) never block.
//
// line should already include its trailing newline.
func appendWithLock(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	_, err = f.Write(line)
	return err
}
