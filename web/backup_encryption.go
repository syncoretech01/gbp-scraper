package web

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode"

	"golang.org/x/crypto/scrypt"
)

// Encrypted backup container.
//
// A backup may hold every business record, note, and job configuration in the
// workspace, so an operator who copies one to another disk needs it encrypted.
// The container is deliberately simple and self-describing so it can be read
// back by this application without a key file or an external tool:
//
//	"GMSBAK01"            8-byte magic and version
//	salt                  16 bytes, scrypt salt
//	nonce prefix          8 bytes, random per file
//	chunk*                4-byte little-endian ciphertext length, then the
//	                      AES-256-GCM ciphertext of at most 1 MiB of plaintext
//
// Each chunk is sealed with nonce = prefix || uint32(index) and additional
// data = magic || index || final-flag, so a truncated, reordered, or spliced
// file fails authentication instead of decrypting into a partial database.
const (
	backupCipherMagic     = "GMSBAK01"
	backupCipherSaltSize  = 16
	backupNoncePrefixSize = 8
	backupChunkSize       = 1 << 20

	// scrypt parameters. N=2^15 with r=8 costs roughly 32 MiB and a fraction of
	// a second on a workstation, which is a sensible floor for a passphrase
	// that protects a whole local database.
	backupScryptN      = 1 << 15
	backupScryptR      = 8
	backupScryptP      = 1
	backupScryptKeyLen = 32

	minimumBackupPassphrase = 12
	maximumBackupPassphrase = 512
)

// ErrBackupPassphrase reports a passphrase that cannot decrypt a container.
var ErrBackupPassphrase = errors.New("the backup passphrase is incorrect or the file is damaged")

// validateBackupPassphrase bounds an operator-supplied passphrase.
func validateBackupPassphrase(passphrase string) error {
	if len(passphrase) < minimumBackupPassphrase || len(passphrase) > maximumBackupPassphrase {
		return fmt.Errorf("the backup passphrase must contain %d to %d characters",
			minimumBackupPassphrase, maximumBackupPassphrase)
	}
	for _, character := range passphrase {
		if unicode.IsControl(character) {
			return errors.New("the backup passphrase cannot contain control characters")
		}
	}

	return nil
}

// backupCipher derives the chunk cipher for one container.
func backupCipher(passphrase string, salt []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(passphrase), salt, backupScryptN, backupScryptR, backupScryptP, backupScryptKeyLen)
	if err != nil {
		return nil, fmt.Errorf("derive backup key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create backup cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create backup cipher mode: %w", err)
	}

	return aead, nil
}

// backupChunkNonce builds the nonce for one chunk from the file prefix.
func backupChunkNonce(aead cipher.AEAD, prefix []byte, index uint32) []byte {
	nonce := make([]byte, aead.NonceSize())
	copy(nonce, prefix)
	binary.LittleEndian.PutUint32(nonce[len(nonce)-4:], index)

	return nonce
}

// backupChunkAAD binds a chunk to its position and to whether it ends the file.
func backupChunkAAD(index uint32, final bool) []byte {
	data := make([]byte, 0, len(backupCipherMagic)+5)
	data = append(data, backupCipherMagic...)
	data = binary.LittleEndian.AppendUint32(data, index)
	if final {
		return append(data, 1)
	}

	return append(data, 0)
}

// encryptBackupFile rewrites path as an encrypted container. The plaintext is
// replaced only after the container has been written and flushed, so a failure
// never leaves the workspace without a readable backup.
func encryptBackupFile(path, passphrase string) error {
	if err := validateBackupPassphrase(passphrase); err != nil {
		return err
	}
	salt := make([]byte, backupCipherSaltSize)
	prefix := make([]byte, backupNoncePrefixSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("create backup salt: %w", err)
	}
	if _, err := rand.Read(prefix); err != nil {
		return fmt.Errorf("create backup nonce: %w", err)
	}
	aead, err := backupCipher(passphrase, salt)
	if err != nil {
		return err
	}

	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open backup for encryption: %w", err)
	}
	defer source.Close()

	temporary := path + ".enc"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create encrypted backup: %w", err)
	}

	writeErr := writeEncryptedBackup(output, source, aead, salt, prefix)
	if syncErr := output.Sync(); writeErr == nil {
		writeErr = syncErr
	}
	if closeErr := output.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(temporary)

		return fmt.Errorf("write encrypted backup: %w", writeErr)
	}
	if err := source.Close(); err != nil {
		_ = os.Remove(temporary)

		return fmt.Errorf("close plaintext backup: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)

		return fmt.Errorf("publish encrypted backup: %w", err)
	}

	return nil
}

func writeEncryptedBackup(output io.Writer, source io.Reader, aead cipher.AEAD, salt, prefix []byte) error {
	if _, err := output.Write([]byte(backupCipherMagic)); err != nil {
		return err
	}
	if _, err := output.Write(salt); err != nil {
		return err
	}
	if _, err := output.Write(prefix); err != nil {
		return err
	}

	plaintext := make([]byte, backupChunkSize)
	var index uint32
	for {
		read, err := io.ReadFull(source, plaintext)
		final := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		if err != nil && !final {
			return err
		}
		sealed := aead.Seal(nil, backupChunkNonce(aead, prefix, index), plaintext[:read], backupChunkAAD(index, final))
		header := binary.LittleEndian.AppendUint32(nil, uint32(len(sealed)))
		if _, err := output.Write(header); err != nil {
			return err
		}
		if _, err := output.Write(sealed); err != nil {
			return err
		}
		if final {
			return nil
		}
		index++
	}
}

// decryptBackupTo streams a container's plaintext to destination. Every chunk
// is authenticated, so a partially written plaintext can only come from an I/O
// failure, never from a wrong passphrase.
func decryptBackupTo(destination io.Writer, path, passphrase string) error {
	if err := validateBackupPassphrase(passphrase); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open encrypted backup: %w", err)
	}
	defer file.Close()

	header := make([]byte, len(backupCipherMagic)+backupCipherSaltSize+backupNoncePrefixSize)
	if _, err := io.ReadFull(file, header); err != nil {
		return ErrBackupPassphrase
	}
	if string(header[:len(backupCipherMagic)]) != backupCipherMagic {
		return errors.New("this backup is not an encrypted container")
	}
	salt := header[len(backupCipherMagic) : len(backupCipherMagic)+backupCipherSaltSize]
	prefix := header[len(backupCipherMagic)+backupCipherSaltSize:]

	aead, err := backupCipher(passphrase, salt)
	if err != nil {
		return err
	}

	lengthBuffer := make([]byte, 4)
	var index uint32
	for {
		if _, err := io.ReadFull(file, lengthBuffer); err != nil {
			return ErrBackupPassphrase
		}
		length := binary.LittleEndian.Uint32(lengthBuffer)
		if int(length) > backupChunkSize+aead.Overhead() {
			return ErrBackupPassphrase
		}
		sealed := make([]byte, length)
		if _, err := io.ReadFull(file, sealed); err != nil {
			return ErrBackupPassphrase
		}
		nonce := backupChunkNonce(aead, prefix, index)
		plaintext, openErr := aead.Open(nil, nonce, sealed, backupChunkAAD(index, true))
		final := openErr == nil
		if openErr != nil {
			plaintext, openErr = aead.Open(nil, nonce, sealed, backupChunkAAD(index, false))
			if openErr != nil {
				return ErrBackupPassphrase
			}
		}
		if _, err := destination.Write(plaintext); err != nil {
			return err
		}
		if final {
			return nil
		}
		index++
	}
}

// backupFileEncrypted reports whether a stored backup is an encrypted
// container, so the download handler knows whether a passphrase is needed.
func backupFileEncrypted(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	magic := make([]byte, len(backupCipherMagic))
	if _, err := io.ReadFull(file, magic); err != nil {
		return false
	}

	return string(magic) == backupCipherMagic
}
