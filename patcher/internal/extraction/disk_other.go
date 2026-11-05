//go:build !linux

package extraction

// checkSpace is a no-op on platforms without a portable free-space probe; the
// copy itself still fails closed if the filesystem is full.
func checkSpace(dir string, need int64) error { return nil }
