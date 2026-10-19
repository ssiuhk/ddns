package shared

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
)

// Dumps and backups are encrypted with age (https://age-encryption.org)
// using a passphrase, so they can also be decrypted with the age tool:
// age -d ddns-backup-....json.gz.age | gunzip

// ageHeader starts every file encrypted by age.
var ageHeader = []byte("age-encryption.org/v1\n")

// ErrNoPassphrase is returned for encrypted data when no passphrase is set.
var ErrNoPassphrase = errors.New("the data is encrypted: set DDNS_BACKUP_PASSPHRASE or DDNS_BACKUP_PASSPHRASE_FILE, or pass --passphrase-file")

// LoadPassphrase returns the passphrase for dumps and backups: the content
// of file if given, else DDNS_BACKUP_PASSPHRASE, else the content of the
// file in DDNS_BACKUP_PASSPHRASE_FILE. An empty result means no encryption.
func LoadPassphrase(file string) (string, error) {
	if file == "" {
		if passphrase := os.Getenv("DDNS_BACKUP_PASSPHRASE"); passphrase != "" {
			return passphrase, nil
		}
		file = os.Getenv("DDNS_BACKUP_PASSPHRASE_FILE")
	}
	if file == "" {
		return "", nil
	}

	content, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("could not read the passphrase: %v", err)
	}
	passphrase := strings.TrimRight(string(content), "\r\n")
	if passphrase == "" {
		return "", fmt.Errorf("the passphrase file %s is empty", file)
	}
	return passphrase, nil
}

// encryptingWriter wraps w so that everything written is encrypted with
// passphrase. Close finishes the encryption but does not close w.
func encryptingWriter(w io.Writer, passphrase string) (io.WriteCloser, error) {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	return age.Encrypt(w, recipient)
}

// decryptingReader returns a reader of the plain data in r, which may be
// encrypted with age or not.
func decryptingReader(r *bufio.Reader, passphrase string) (io.Reader, error) {
	header, _ := r.Peek(len(ageHeader))
	if !bytes.Equal(header, ageHeader) {
		return r, nil
	}
	if passphrase == "" {
		return nil, ErrNoPassphrase
	}

	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	plain, err := age.Decrypt(r, identity)
	if err != nil {
		return nil, fmt.Errorf("could not decrypt the data, is the passphrase right? %v", err)
	}
	return plain, nil
}
