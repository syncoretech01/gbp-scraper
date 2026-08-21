package web

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A backup can hold every record in the workspace, so the container has to be
// genuinely authenticated: a wrong passphrase, a truncated file, or a spliced
// chunk must all fail rather than yield a partial database.

func TestEncryptedBackupRoundTripsAndRejectsTampering(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "backup.db")

	// Larger than one chunk so the multi-chunk path, and the final-chunk
	// marker that closes it, are both exercised.
	plaintext := bytes.Repeat([]byte("SQLite format 3\x00local workspace backup"), 40_000)
	if err := os.WriteFile(path, plaintext, 0o600); err != nil {
		t.Fatal(err)
	}
	if len(plaintext) <= backupChunkSize {
		t.Fatalf("fixture is %d bytes; it must exceed the %d byte chunk size", len(plaintext), backupChunkSize)
	}

	passphrase := "correct horse battery staple"
	if err := encryptBackupFile(path, passphrase); err != nil {
		t.Fatal(err)
	}

	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("SQLite format 3")) {
		t.Fatal("the encrypted container still contains recognisable plaintext")
	}
	if !backupFileEncrypted(path) {
		t.Fatal("the encrypted container is not recognised as encrypted")
	}

	var decrypted bytes.Buffer
	if err := decryptBackupTo(&decrypted, path, passphrase); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted.Bytes(), plaintext) {
		t.Fatalf("round-tripped %d bytes, want %d", decrypted.Len(), len(plaintext))
	}

	var wrong bytes.Buffer
	err = decryptBackupTo(&wrong, path, "incorrect horse battery staple")
	if !errors.Is(err, ErrBackupPassphrase) {
		t.Fatalf("wrong passphrase error = %v, want %v", err, ErrBackupPassphrase)
	}
	if wrong.Len() != 0 {
		t.Fatalf("a wrong passphrase produced %d plaintext bytes", wrong.Len())
	}

	// Truncating the container must be detected: the final-chunk marker is part
	// of the authenticated data, so a shortened file cannot authenticate.
	truncated := filepath.Join(directory, "truncated.gmsbak")
	if err := os.WriteFile(truncated, ciphertext[:len(ciphertext)-64], 0o600); err != nil {
		t.Fatal(err)
	}
	var partial bytes.Buffer
	if err := decryptBackupTo(&partial, truncated, passphrase); err == nil {
		t.Fatal("a truncated container decrypted successfully")
	}
}

func TestBackupPassphraseIsBounded(t *testing.T) {
	t.Parallel()

	for _, passphrase := range []string{"", "short", "with\ncontrol characters"} {
		if err := validateBackupPassphrase(passphrase); err == nil {
			t.Fatalf("passphrase %q was accepted", passphrase)
		}
	}
	if err := validateBackupPassphrase(strings.Repeat("a", maximumBackupPassphrase+1)); err == nil {
		t.Fatal("an over-long passphrase was accepted")
	}
	if err := validateBackupPassphrase("a local workspace passphrase"); err != nil {
		t.Fatalf("a reasonable passphrase was rejected: %v", err)
	}
}
