package shared

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWritePrivateFileReplacesPermissiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dump.json")
	assert.Nil(t, os.WriteFile(path, []byte("old"), 0644))

	assert.Nil(t, WritePrivateFile(path, func(w io.Writer) error {
		_, err := io.WriteString(w, "new")
		return err
	}))

	info, err := os.Stat(path)
	assert.Nil(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	data, _ := os.ReadFile(path)
	assert.Equal(t, "new", string(data))

	entries, _ := os.ReadDir(filepath.Dir(path))
	assert.Len(t, entries, 1)
}
