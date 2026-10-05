package main

import (
	"path/filepath"
	"strings"
	"sync"
)

type loginCredentialState struct {
	mu        sync.Mutex
	revisions map[string]uint64
}

func (a *App) loginPasswordStore() loginPasswordStore {
	a.settingsMu.Lock()
	path := a.settingsFile()
	a.settingsMu.Unlock()
	dir := ""
	if path != "" {
		dir = filepath.Join(filepath.Dir(path), "login-passwords")
	}
	return loginPasswordStore{dir: dir, available: keyProtectionAvailable(), protect: protectBytes, unprotect: unprotectBytes}
}

// LoginPasswordStatus exposes presence only, never password values.
type LoginPasswordStatus struct {
	Supported    bool   `json:"supported"`
	Available    bool   `json:"available"`
	AccountSaved bool   `json:"account_saved"`
	ServerSaved  bool   `json:"server_saved"`
	Error        string `json:"error,omitempty"`
}

func (a *App) GetLoginPasswordStatus(addr, nickname string) LoginPasswordStatus {
	addr, nickname = strings.TrimSpace(addr), strings.TrimSpace(nickname)
	a.loginCredentials.mu.Lock()
	defer a.loginCredentials.mu.Unlock()
	s := a.loginPasswordStore()
	status := LoginPasswordStatus{Supported: true, Available: s.available && s.dir != ""}
	if addr == "" || nickname == "" {
		return status
	}
	passwords, err := s.load(addr, nickname)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.AccountSaved = passwords.Password != ""
	status.ServerSaved = passwords.ServerPassword != ""
	return status
}

// ForgetLoginPasswords also invalidates a pending successful connect's save.
func (a *App) ForgetLoginPasswords(addr, nickname string) string {
	addr, nickname = strings.TrimSpace(addr), strings.TrimSpace(nickname)
	a.loginCredentials.mu.Lock()
	defer a.loginCredentials.mu.Unlock()
	if a.loginCredentials.revisions == nil {
		a.loginCredentials.revisions = make(map[string]uint64)
	}
	a.loginCredentials.revisions[loginPasswordKey(addr, nickname)]++
	if err := a.loginPasswordStore().remove(addr, nickname); err != nil {
		return err.Error()
	}
	return ""
}

type LoginRequest struct {
	Bookmark          string `json:"bookmark"`
	Addr              string `json:"addr"`
	Nickname          string `json:"nickname"`
	DisplayName       string `json:"display_name"`
	Password          string `json:"password"`
	ServerPassword    string `json:"server_password"`
	RememberPasswords bool   `json:"remember_passwords"`
	UseSavedAccount   bool   `json:"use_saved_account"`
	UseSavedServer    bool   `json:"use_saved_server"`
}

// ConnectLogin keeps stored passwords entirely in the native process. Explicit
// flags distinguish "use saved" from an intentionally empty guest password.
func (a *App) ConnectLogin(request LoginRequest) ConnectTabResult {
	request.Addr, request.Nickname = strings.TrimSpace(request.Addr), strings.TrimSpace(request.Nickname)
	if request.Addr == "" || request.Nickname == "" || len(request.Addr) > 2048 || len(request.Nickname) > 256 {
		return ConnectTabResult{Error: "server address and account / guest name are required"}
	}
	passwords := loginPasswords{Password: request.Password, ServerPassword: request.ServerPassword}
	key := loginPasswordKey(request.Addr, request.Nickname)
	a.loginCredentials.mu.Lock()
	revision := a.loginCredentials.revisions[key]
	if request.UseSavedAccount || request.UseSavedServer {
		saved, err := a.loginPasswordStore().load(request.Addr, request.Nickname)
		if err != nil {
			a.loginCredentials.mu.Unlock()
			return ConnectTabResult{Error: err.Error()}
		}
		if request.UseSavedAccount {
			passwords.Password = saved.Password
		}
		if request.UseSavedServer {
			passwords.ServerPassword = saved.ServerPassword
		}
		if request.UseSavedAccount && passwords.Password == "" || request.UseSavedServer && passwords.ServerPassword == "" {
			a.loginCredentials.mu.Unlock()
			return ConnectTabResult{Error: "saved password is no longer available; enter it again"}
		}
	}
	a.loginCredentials.mu.Unlock()
	result := a.ConnectNamedBookmarkTabWithID(request.Bookmark, request.Addr, request.Nickname, request.DisplayName, passwords.Password, passwords.ServerPassword)
	if result.Error != "" {
		return result
	}
	a.tabsMu.Lock()
	if tab := a.tabs[result.TabID]; tab != nil {
		tab.loginPasswords = &passwords
	}
	a.tabsMu.Unlock()
	if request.RememberPasswords {
		a.loginCredentials.mu.Lock()
		defer a.loginCredentials.mu.Unlock()
		if a.loginCredentials.revisions[key] == revision {
			if err := a.loginPasswordStore().save(request.Addr, request.Nickname, passwords); err != nil {
				result.Warning = "Connected, but passwords could not be saved: " + err.Error()
			}
		}
	}
	return result
}
