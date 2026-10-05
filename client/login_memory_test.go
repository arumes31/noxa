package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectLoginUsesProtectedPasswordsAndHonorsForgetDuringConnect(t *testing.T) {
	if !keyProtectionAvailable() {
		t.Skip("OS password protection is unavailable on this platform")
	}
	for _, forget := range []bool{false, true} {
		name := "remember"
		if forget {
			name = "forget while authenticating"
		}
		t.Run(name, func(t *testing.T) {
			a := identityTestApp(t, "off")
			addr, requests, release := gatedReconnectServer(t)
			passwords := loginPasswords{Password: "test-account-secret", ServerPassword: "test-server-secret"}
			if err := a.loginPasswordStore().save(addr, "owner", passwords); err != nil {
				t.Fatal(err)
			}
			results := make(chan ConnectTabResult, 1)
			go func() {
				results <- a.ConnectLogin(LoginRequest{Addr: addr, Nickname: "owner", DisplayName: "Public name",
					UseSavedAccount: true, UseSavedServer: true, RememberPasswords: true})
			}()
			request := waitReconnect(t, requests)
			if request.Username != "owner" || request.Nickname != "Public name" || request.Password != passwords.Password || request.ServerPassword != passwords.ServerPassword {
				t.Fatal("saved login did not authenticate with the correct scoped credentials")
			}
			if forget {
				if err := a.ForgetLoginPasswords(addr, "owner"); err != "" {
					t.Fatal(err)
				}
			}
			close(release)
			result := waitReconnect(t, results)
			if result.Error != "" || result.Warning != "" {
				t.Fatalf("login failed: %s / %s", result.Error, result.Warning)
			}
			defer a.CloseTab(result.TabID)
			status := a.GetLoginPasswordStatus(addr, "owner")
			if status.AccountSaved == forget || status.ServerSaved == forget || status.Error != "" {
				t.Fatalf("unexpected saved-password status: %+v", status)
			}
			a.tabsMu.Lock()
			cached := a.tabs[result.TabID].loginPasswords
			a.tabsMu.Unlock()
			if cached == nil || *cached != passwords {
				t.Fatal("active connection did not retain native reconnect credentials")
			}
			settings := a.GetSettings()
			if len(settings.Recents) != 1 || settings.Recents[0].DisplayName != "Public name" || settings.Recents[0].Nickname != "owner" {
				t.Fatal("successful profile was not remembered")
			}
			serialized, err := json.Marshal([]any{settings, status, result, a.ListTabs()})
			if err != nil || strings.Contains(string(serialized), passwords.Password) || strings.Contains(string(serialized), passwords.ServerPassword) {
				t.Fatal("native password escaped into a frontend response")
			}
		})
	}
}

func TestFailedLoginPreservesPreviousProfileAndPasswords(t *testing.T) {
	a := identityTestApp(t, "off")
	const addr = "127.0.0.1:0"
	a.recordRecentConnection("previous.example", "previous", "Previous name")
	passwords := loginPasswords{Password: "previous-test-secret"}
	if keyProtectionAvailable() {
		if err := a.loginPasswordStore().save(addr, "owner", passwords); err != nil {
			t.Fatal(err)
		}
	}
	result := a.ConnectLogin(LoginRequest{Addr: addr, Nickname: "owner", Password: "rejected-secret", RememberPasswords: true})
	if result.Error == "" {
		t.Fatal("invalid endpoint accepted")
	}
	if recent := a.GetSettings().Recents; len(recent) != 1 || recent[0].Addr != "previous.example" {
		t.Fatal("failed login replaced previous successful profile")
	}
	if keyProtectionAvailable() {
		got, err := a.loginPasswordStore().load(addr, "owner")
		if err != nil || got != passwords {
			t.Fatal("failed login replaced previously saved password")
		}
	}
	if result := a.ConnectLogin(LoginRequest{Addr: addr, Nickname: "other", UseSavedAccount: true}); result.Error == "" || !strings.Contains(result.Error, "saved password") {
		t.Fatal("missing saved password did not fail closed")
	}
}

func TestReconnectRetainsNativeLoginPasswordsInReplacement(t *testing.T) {
	addr, requests, release := gatedReconnectServer(t)
	a, tabID, source, _ := prepareReconnect(t, addr)
	passwords := loginPasswords{Password: "native-account-secret", ServerPassword: "native-server-secret"}
	source.loginPasswords = &passwords
	results := make(chan ConnectTabResult, 1)
	go func() { results <- a.ReconnectTab(tabID, "", "") }()
	request := waitReconnect(t, requests)
	if request.Password != passwords.Password || request.ServerPassword != passwords.ServerPassword {
		t.Fatal("reconnect did not use native credentials")
	}
	close(release)
	if result := waitReconnect(t, results); result.Error != "" {
		t.Fatal(result.Error)
	}
	a.tabsMu.Lock()
	defer a.tabsMu.Unlock()
	replacement := a.tabs[tabID]
	if replacement == source || replacement.loginPasswords == nil || *replacement.loginPasswords != passwords {
		t.Fatal("a second reconnect would lose saved login credentials")
	}
}
