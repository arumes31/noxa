package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Passwords never enter Settings or a frontend response. Only OS-protected
// envelopes are written, and each envelope authenticates its connection scope.
type loginPasswords struct {
	Password       string `json:"password"`
	ServerPassword string `json:"server_password"`
}

type loginPasswordEnvelope struct {
	Addr     string `json:"addr"`
	Nickname string `json:"nickname"`
	loginPasswords
}

type loginPasswordStore struct {
	dir                string
	available          bool
	protect, unprotect func([]byte) ([]byte, error)
}

func loginPasswordKey(addr, nickname string) string {
	raw, _ := json.Marshal([2]string{addr, nickname})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (s loginPasswordStore) path(addr, nickname string) string {
	return filepath.Join(s.dir, loginPasswordKey(addr, nickname)+".protected")
}

func (s loginPasswordStore) load(addr, nickname string) (loginPasswords, error) {
	var empty loginPasswords
	if s.dir == "" {
		return empty, errors.New("password storage is unavailable")
	}
	root, name, err := openParentRoot(s.path(addr, nickname))
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer root.Close()
	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer f.Close()
	if !s.available {
		return empty, errors.New("OS password protection is unavailable")
	}
	blob, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(blob) > 65536 {
		return empty, errors.New("cannot read protected passwords")
	}
	plain, err := s.unprotect(blob)
	if err != nil {
		return empty, errors.New("saved passwords cannot be unlocked by this OS account; enter them again")
	}
	defer clear(plain)
	var envelope loginPasswordEnvelope
	if json.Unmarshal(plain, &envelope) != nil || envelope.Addr != addr || envelope.Nickname != nickname {
		return empty, errors.New("saved passwords do not match this connection")
	}
	return envelope.loginPasswords, nil
}

func (s loginPasswordStore) save(addr, nickname string, passwords loginPasswords) error {
	if !s.available || s.dir == "" {
		return errors.New("OS password protection is unavailable")
	}
	if len(passwords.Password) > 4096 || len(passwords.ServerPassword) > 4096 {
		return errors.New("password is too long to store")
	}
	if passwords == (loginPasswords{}) {
		return s.remove(addr, nickname)
	}
	raw, err := json.Marshal(loginPasswordEnvelope{Addr: addr, Nickname: nickname, loginPasswords: passwords})
	if err != nil {
		return err
	}
	defer clear(raw)
	blob, err := s.protect(raw)
	if err != nil {
		return errors.New("OS password protection failed; passwords were not saved")
	}
	return writePrivateFileAtomic(s.path(addr, nickname), blob)
}

func (s loginPasswordStore) remove(addr, nickname string) error {
	if s.dir == "" {
		return errors.New("password storage is unavailable")
	}
	root, name, err := openParentRoot(s.path(addr, nickname))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Remove(name); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
