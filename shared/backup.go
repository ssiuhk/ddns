package shared

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	backupPrefix     = "ddns-backup-"
	backupSuffix     = ".json.gz"
	encryptedSuffix  = ".age"
	backupTimeFormat = "20060102T150405Z"
)

// WriteBackup stores a compressed snapshot in dir and returns its path,
// encrypted with passphrase unless it is empty. The file only appears under
// its final name once it is complete.
func WriteBackup(dir string, snapshot *Snapshot, now time.Time, passphrase string) (string, error) {
	name := backupPrefix + now.UTC().Format(backupTimeFormat) + backupSuffix
	if passphrase != "" {
		name += encryptedSuffix
	}
	path := filepath.Join(dir, name)

	tmp, err := os.CreateTemp(dir, ".tmp-"+backupPrefix)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	if err := WriteSnapshot(tmp, snapshot, true, passphrase); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	return path, os.Rename(tmp.Name(), path)
}

// PruneBackups deletes the backups in dir that were taken more than maxAge
// before now, and returns the deleted paths. Only files named like the ones
// WriteBackup creates are considered.
func PruneBackups(dir string, maxAge time.Duration, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var deleted []string
	for _, entry := range entries {
		name := entry.Name()
		stamp := strings.TrimSuffix(strings.TrimSuffix(name, encryptedSuffix), backupSuffix)
		if entry.IsDir() || !strings.HasPrefix(name, backupPrefix) || stamp == strings.TrimSuffix(name, encryptedSuffix) {
			continue
		}

		taken, err := time.Parse(backupTimeFormat, strings.TrimPrefix(stamp, backupPrefix))
		if err != nil || now.Sub(taken) <= maxAge {
			continue
		}

		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil {
			return deleted, fmt.Errorf("could not delete %s: %v", path, err)
		}
		deleted = append(deleted, path)
	}

	return deleted, nil
}

// LatestBackup returns the path of the newest backup in dir.
func LatestBackup(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	latest := ""
	for _, entry := range entries {
		name := entry.Name()
		// the timestamp in the name sorts in time order
		if !entry.IsDir() && strings.HasPrefix(name, backupPrefix) && strings.Contains(name, backupSuffix) && name > latest {
			latest = name
		}
	}
	if latest == "" {
		return "", fmt.Errorf("there is no backup in %s", dir)
	}
	return filepath.Join(dir, latest), nil
}
