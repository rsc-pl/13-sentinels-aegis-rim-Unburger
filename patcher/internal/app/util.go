package app

import (
	"encoding/json"
	"os"
	"runtime"

	"13s-patcher/internal/atomic"
)

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }

// writeJSON writes v as indented JSON atomically (overwriting).
func writeJSON(path string, v any) error {
	return atomic.WriteJSON(path, v, atomic.Options{Overwrite: true})
}

func goos() string { return runtime.GOOS }

// fileIsFile reports whether p exists and is a regular file.
func fileIsFile(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// dirExists reports whether p exists and is a directory.
func dirExists(p string) bool {
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
