package shared

import (
	"bytes"
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

	old, err := WriteBackup(dir, snapshot, now.Add(-181*24*time.Hour), "")
	assert.Nil(t, err)
	kept, err := WriteBackup(dir, snapshot, now.Add(-179*24*time.Hour), "")
	assert.Nil(t, err)
	latest, err := WriteBackup(dir, snapshot, now, "")
	assert.Nil(t, err)
	// encrypted backups are pruned the same way
	oldEncrypted, err := WriteBackup(dir, snapshot, now.Add(-200*24*time.Hour), "secret")
	assert.Nil(t, err)
	assert.Equal(t, ".age", filepath.Ext(oldEncrypted))
	encrypted, err := WriteBackup(dir, snapshot, now.Add(-time.Hour), "secret")
	assert.Nil(t, err)

	// unrelated files are never touched
	other := filepath.Join(dir, "notes.txt")
	assert.Nil(t, os.WriteFile(other, []byte("keep"), 0o600))

	deleted, err := PruneBackups(dir, 180*24*time.Hour, now)
	assert.Nil(t, err)
	assert.ElementsMatch(t, []string{old, oldEncrypted}, deleted)

	for _, path := range []string{kept, latest, encrypted, other} {
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
	read, err := ReadSnapshot(file, "")
	assert.Nil(t, err)
	assert.Equal(t, snapshot.Entries, read.Entries)
}

func TestEncryptedSnapshots(t *testing.T) {
	snapshot := &Snapshot{Version: snapshotVersion, Entries: []SnapshotItem{
		{Key: "dnssec:d.example.org:keys", Hash: map[string]string{"1": "private key"}},
	}}

	for _, compress := range []bool{false, true} {
		var buffer bytes.Buffer
		assert.Nil(t, WriteSnapshot(&buffer, snapshot, compress, "correct horse"))
		assert.NotContains(t, buffer.String(), "private key")
		data := buffer.Bytes()

		_, err := ReadSnapshot(bytes.NewReader(data), "")
		assert.Equal(t, ErrNoPassphrase, err)

		_, err = ReadSnapshot(bytes.NewReader(data), "wrong")
		assert.ErrorContains(t, err, "passphrase")

		read, err := ReadSnapshot(bytes.NewReader(data), "correct horse")
		assert.Nil(t, err)
		assert.Equal(t, snapshot.Entries, read.Entries)
	}

	// plain data is read even when a passphrase is set
	var buffer bytes.Buffer
	assert.Nil(t, WriteSnapshot(&buffer, snapshot, true, ""))
	read, err := ReadSnapshot(&buffer, "correct horse")
	assert.Nil(t, err)
	assert.Equal(t, snapshot.Entries, read.Entries)
}

func TestLoadPassphrase(t *testing.T) {
	t.Setenv("DDNS_BACKUP_PASSPHRASE", "")
	t.Setenv("DDNS_BACKUP_PASSPHRASE_FILE", "")
	passphrase, err := LoadPassphrase("")
	assert.Nil(t, err)
	assert.Equal(t, "", passphrase)

	file := filepath.Join(t.TempDir(), "passphrase")
	assert.Nil(t, os.WriteFile(file, []byte("from file\n"), 0o600))
	t.Setenv("DDNS_BACKUP_PASSPHRASE_FILE", file)
	passphrase, _ = LoadPassphrase("")
	assert.Equal(t, "from file", passphrase)

	t.Setenv("DDNS_BACKUP_PASSPHRASE", "from env")
	passphrase, _ = LoadPassphrase("")
	assert.Equal(t, "from env", passphrase)

	// an explicit file wins
	passphrase, _ = LoadPassphrase(file)
	assert.Equal(t, "from file", passphrase)

	_, err = LoadPassphrase(file + ".missing")
	assert.NotNil(t, err)
}
