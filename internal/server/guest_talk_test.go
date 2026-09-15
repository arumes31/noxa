package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/config"
	"noxa/internal/permissions"
	"noxa/internal/state"
)

func TestGuestTalkRespectsChannelRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		needed                    int
		negate, unavailable, want bool
	}{
		{name: "open", want: true},
		{name: "explicit deny", negate: true},
		{name: "required talk power", needed: 10},
		{name: "permission backend failure", unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tp := permissions.NewTieredPermissions()
			channel := permissions.NewPermissionSet()
			channel.Set(&permissions.Permission{Key: permissions.PermissionKeyClientNeededTalkPower, Type: permissions.PermissionTypeInteger, Value: tc.needed})
			if tc.negate {
				channel.Set(&permissions.Permission{Key: permissions.PermissionKeyClientTalkPower, Type: permissions.PermissionTypeInteger, Negate: true})
			}
			tp.Set(permissions.TierChannel, channel)
			fp := &fakePerms{loadForClientFn: func(_ context.Context, userID, channelID int64) (permissions.TieredPermissions, error) {
				if userID != 0 || channelID != 9 {
					t.Errorf("loaded user/channel %d/%d", userID, channelID)
				}
				if tc.unavailable {
					return permissions.TieredPermissions{}, errors.New("unavailable")
				}
				return tp, nil
			}}
			manager := state.New(testLogger())
			manager.AddClient(&state.Client{ClientID: "guest", ChannelID: 9})
			s := New(&config.Config{}, testLogger(), &Deps{Perms: fp, Resolver: permissions.NewResolver(), State: manager})
			guest := &Client{ID: "guest"}
			guest.setIdentity("guest:test", "guest", 0, false)
			s.register(guest)
			if got := s.canTalk(guest.ID); got != tc.want {
				t.Errorf("canTalk = %v, want %v", got, tc.want)
			}
			if _, exists := tp.Get(permissions.TierServerGroup); exists {
				t.Error("mutated cached channel permissions")
			}
		})
	}
}

func TestTalkPowerThreshold(t *testing.T) {
	for _, tc := range []struct {
		name          string
		power, needed int
		admin, want   bool
	}{
		{"open default", 0, 0, false, true},
		{"insufficient", 0, 10, false, false},
		{"equal", 10, 10, false, true},
		{"above", 20, 10, false, true},
		{"admin", 0, 10, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tp := permissions.NewTieredPermissions()
			set := permissions.NewPermissionSet()
			set.Set(&permissions.Permission{Key: permissions.PermissionKeyClientTalkPower, Type: permissions.PermissionTypeInteger, Value: tc.power})
			set.Set(&permissions.Permission{Key: permissions.PermissionKeyClientNeededTalkPower, Type: permissions.PermissionTypeInteger, Value: tc.needed})
			tp.Set(permissions.TierChannel, set)
			p := &permChecker{resolver: permissions.NewResolver(), tp: tp, admin: tc.admin}
			if got := p.talkAllowed(); got != tc.want {
				t.Errorf("talkAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}
