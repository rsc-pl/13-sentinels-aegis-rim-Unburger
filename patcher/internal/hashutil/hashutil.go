// Package hashutil provides SHA-256 helpers used throughout the patcher.
package hashutil

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// Bytes returns the lowercase hex SHA-256 of b.
func Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// File streams the lowercase hex SHA-256 of a file and returns (hex, size).
func File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Short truncates a hex digest for display.
func Short(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
}
