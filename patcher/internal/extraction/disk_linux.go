//go:build linux

package extraction

import (
	"fmt"
	"syscall"
)

// checkSpace fails when dir's filesystem has less than need free bytes. It only
// guards the small, regenerable cache, never the user's own containers.
func checkSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil // best effort: an unavailable probe must not block the build
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < need {
		return fmt.Errorf("not enough free space in %s: %d bytes free, %d bytes required", dir, free, need)
	}
	return nil
}
