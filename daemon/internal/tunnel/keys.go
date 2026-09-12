package tunnel

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
)

const maxKeyBytes = 1 << 20

type unlockedKey struct {
	digest [32]byte
	signer ssh.Signer
}

// KeyStore retains parsed signers only; passwords are never stored.
type KeyStore struct {
	mu   sync.Mutex
	keys map[string]unlockedKey
}

func NewKeyStore() *KeyStore { return &KeyStore{keys: make(map[string]unlockedKey)} }

func readPrivateKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxKeyBytes {
		return nil, errors.New("private key must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxKeyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxKeyBytes {
		return nil, errors.New("private key exceeds size limit")
	}
	return data, nil
}

func (s *KeyStore) cached(path string, data []byte) ssh.Signer {
	if s == nil {
		return nil
	}
	path = filepath.Clean(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[path]
	if ok && entry.digest == sha256.Sum256(data) {
		return entry.signer
	}
	delete(s.keys, path)
	return nil
}

func (s *KeyStore) Load(path string) (ssh.Signer, error) {
	data, err := readPrivateKey(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key %s: %w", path, err)
	}
	defer clear(data)
	if signer := s.cached(path, data); signer != nil {
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey(data)
	var encrypted *ssh.PassphraseMissingError
	if errors.As(err, &encrypted) {
		return nil, fmt.Errorf("private key is locked; unlock it in the client: %w", err)
	}
	return signer, err
}

func (s *KeyStore) Unlock(path string, passphrase []byte) error {
	data, err := readPrivateKey(path)
	if err != nil {
		return err
	}
	defer clear(data)
	signer, err := ssh.ParsePrivateKeyWithPassphrase(data, passphrase)
	if err != nil {
		return errors.New("unable to unlock private key; check the key format and passphrase")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[filepath.Clean(path)] = unlockedKey{digest: sha256.Sum256(data), signer: signer}
	return nil
}

func (s *KeyStore) Lock(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, filepath.Clean(path))
}

func (s *KeyStore) Clear() { s.mu.Lock(); defer s.mu.Unlock(); clear(s.keys) }

func (s *KeyStore) KeyStatus(path string) (encrypted, unlocked bool) {
	data, err := readPrivateKey(path)
	if err != nil {
		return false, false
	}
	defer clear(data)
	_, err = ssh.ParsePrivateKey(data)
	var protected *ssh.PassphraseMissingError
	return errors.As(err, &protected), s.cached(path, data) != nil
}
