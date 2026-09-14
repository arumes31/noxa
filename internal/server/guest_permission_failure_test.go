package server

import (
	"context"
	"errors"
	"testing"

	"voicx/internal/config"
	"voicx/internal/permissions"
)

type unavailableGuestPermissions struct{ fakePerms }

func (*unavailableGuestPermissions) LoadGroupPermissions(context.Context, int64) (permissions.PermissionSet, error) {
	return nil, errors.New("permission store unavailable")
}

func TestGuestPermissionFailureDeniesMedia(t *testing.T) {
	s := New(&config.Config{}, testLogger(), &Deps{
		Perms: &unavailableGuestPermissions{}, Resolver: permissions.NewResolver(), DefaultGuestGroupID: 3,
	})
	guest := &Client{ID: "guest"}
	guest.setIdentity("guest:test", "guest", 0, false)
	s.register(guest)
	if _, err := s.permCheckerFor(context.Background(), guest); err == nil {
		t.Error("permission store failure became default guest permissions")
	}
	if s.canTalk(guest.ID) {
		t.Error("guest audio allowed while configured permissions are unavailable")
	}
	if s.canPublishVideo(guest.ID) {
		t.Error("guest video allowed while configured permissions are unavailable")
	}
	if s.subscribeAllowed(context.Background(), guest, 1) {
		t.Error("guest subscription allowed while configured permissions are unavailable")
	}
}
