// Package atomic writes files in a crash-safe way: content is written to a
// temporary sibling and renamed into place only after the whole buffer is on
// disk. A cancelled or failed operation never leaves a partially valid artifact
// behind.
package atomic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Options controls how a file is written.
type Options struct {
	// Overwrite allows replacing an existing destination. When false and the
	// destination exists, WriteFile returns an error.
	Overwrite bool
	// Perm is the file mode (default 0o644).
	Perm os.FileMode
}

// WriteFile writes data to path atomically.
func WriteFile(path string, data []byte, opts Options) error {
	if path == "" {
		return fmt.Errorf("atomic: empty destination path")
	}
	perm := opts.Perm
	if perm == 0 {
		perm = 0o644
	}
	if !opts.Overwrite {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing file %s (pass --force to replace)", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// WriteJSON marshals v as indented JSON and writes it atomically.
func WriteJSON(path string, v any, opts Options) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return WriteFile(path, raw, opts)
}
