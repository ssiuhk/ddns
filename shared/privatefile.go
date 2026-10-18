package shared

import (
	"io"
	"os"
	"path/filepath"
)

// WritePrivateFile writes a file readable only by its owner. It writes to a
// temporary file first and renames it, so an existing file is replaced with
// the new permissions and never left half written.
func WritePrivateFile(path string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
