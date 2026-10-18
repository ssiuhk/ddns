package shared

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBackupsAreWrittenAndPruned(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	snapshot := &Snapshot{Version: snapshotVersion, Entries: []SnapshotItem{
		{Key: "www", Hash: map[string]string{"ip": "10.0.0.1", "token": "abc"}, TTL: 5000},
	}}

	old, err := WriteBackup(dir, snapshot, now.Add(-181*24*time.Hour))
	assert.Nil(t, err)
	kept, err := WriteBackup(dir, snapshot, now.Add(-179*24*time.Hour))
	assert.Nil(t, err)
	latest, err := WriteBackup(dir, snapshot, now)
	assert.Nil(t, err)

	// unrelated files are never touched
	other := filepath.Join(dir, "notes.txt")
	assert.Nil(t, os.WriteFile(other, []byte("keep"), 0o600))

	deleted, err := PruneBackups(dir, 180*24*time.Hour, now)
	assert.Nil(t, err)
	assert.Equal(t, []string{old}, deleted)

	for _, path := range []string{kept, latest, other} {
		_, err := os.Stat(path)
		assert.Nil(t, err, path)
	}

	newest, err := LatestBackup(dir)
	assert.Nil(t, err)
	assert.Equal(t, latest, newest)
	_, err = LatestBackup(t.TempDir())
	assert.NotNil(t, err)

	// backups read back to the same data
	file, err := os.Open(latest)
	assert.Nil(t, err)
	defer file.Close()
	read, err := ReadSnapshot(file)
	assert.Nil(t, err)
	assert.Equal(t, snapshot.Entries, read.Entries)
}
