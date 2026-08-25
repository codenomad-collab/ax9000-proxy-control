package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode"
	"unicode/utf8"
)

var (
	errCurrentPassword = errors.New("current password is incorrect")
	errPasswordPolicy  = errors.New("新密码需要 8 到 128 个字符，且不能包含控制字符")
	errPasswordSame    = errors.New("新密码不能与当前密码相同")
)

func (a *App) passwordMatches(password string) bool {
	a.credentialMu.RLock()
	defer a.credentialMu.RUnlock()
	return a.cfg.passwordMatches(password)
}

func (a *App) changePassword(currentPassword, newPassword string) error {
	if !validPassword(newPassword) {
		return errPasswordPolicy
	}
	if currentPassword == newPassword {
		return errPasswordSame
	}

	a.credentialMu.Lock()
	defer a.credentialMu.Unlock()

	if !a.cfg.passwordMatches(currentPassword) {
		return errCurrentPassword
	}
	if a.configPath == "" {
		return errors.New("configuration path is unavailable")
	}

	salt, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generate password salt: %w", err)
	}
	hash := sha256.Sum256([]byte(salt + newPassword))

	next := a.cfg
	next.PasswordSalt = salt
	next.PasswordSHA256 = hex.EncodeToString(hash[:])
	if err := writeConfigAtomic(a.configPath, next); err != nil {
		return err
	}
	a.cfg.PasswordSalt = next.PasswordSalt
	a.cfg.PasswordSHA256 = next.PasswordSHA256
	return nil
}

func validPassword(password string) bool {
	length := utf8.RuneCountInString(password)
	if length < 8 || length > 128 {
		return false
	}
	for _, char := range password {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func writeConfigAtomic(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".config.json.*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	removeTemporary = false
	return nil
}
