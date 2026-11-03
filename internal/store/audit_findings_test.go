package store

import (
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"

	"noxa/internal/authorization"
)

// A nonexistent account in a role change is invalid input, so Authority keeps
// serving the current evaluator instead of treating it as an ambiguous commit.
func TestRoleChangeRejectsMissingAccountsAsInvalid(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: p.Revision,
		Role: authorization.Role{Name: "Helper"}})
	if err != nil {
		t.Fatal(err)
	}
	var roleID int64
	for _, r := range p.Roles {
		if r.Name == "Helper" {
			roleID = r.ID
		}
	}
	const missing = int64(1 << 40)
	_, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: p.Revision,
		UserID: missing, RoleIDs: []int64{roleID}})
	if !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("missing member: err = %v, want ErrRoleInvalid", err)
	}
	channel := p.Channels[0]
	channel.Overrides = append(channel.Overrides, authorization.RoleOverride{UserID: missing, Capability: authorization.ViewChannel, Effect: authorization.Allow})
	_, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: p.Revision, Channel: channel})
	if !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("missing override subject: err = %v, want ErrRoleInvalid", err)
	}
	if _, err := s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: p.Revision,
		UserID: member, RoleIDs: []int64{roleID}}); err != nil {
		t.Fatalf("existing member: %v", err)
	}
}

func TestToggleReactionEnforcesLimits(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id, _, err := s.StoreChatMessage(ctx, 0, "author", "author", sealTest(t, testScopeKey(11), "react"), 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := range MaxReactionsPerUser {
		if _, _, err := s.ToggleReaction(ctx, id, "one", fmt.Sprintf("r%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.ToggleReaction(ctx, id, "one", "extra"); !errors.Is(err, ErrReactionLimit) {
		t.Fatalf("per-user limit: err = %v", err)
	}
	// Removing an existing reaction is always allowed.
	if _, added, err := s.ToggleReaction(ctx, id, "one", "r0"); err != nil || added {
		t.Fatalf("removal: added=%v err=%v", added, err)
	}
	for i := MaxReactionsPerUser; i < MaxReactionLabelsPerMessage+1; i++ {
		_, _, err := s.ToggleReaction(ctx, id, fmt.Sprintf("user%d", i), fmt.Sprintf("r%d", i))
		if i < MaxReactionLabelsPerMessage+1-1 && err != nil {
			t.Fatalf("label %d: %v", i, err)
		}
		if i == MaxReactionLabelsPerMessage && !errors.Is(err, ErrReactionLimit) {
			t.Fatalf("per-message limit: err = %v", err)
		}
	}
	// An existing label can still be joined at the cap.
	if _, added, err := s.ToggleReaction(ctx, id, "late", "r1"); err != nil || !added {
		t.Fatalf("joining existing label: added=%v err=%v", added, err)
	}
}

func TestCanonicalDeviceKeyLookup(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	variant := "leading text\r\n" + strings.ReplaceAll(strings.TrimSuffix(canonical, "\n"), "\n", "\r\n") + "\r\ntrailing"
	if got, ok := canonicalDeviceKey(variant); !ok || got != canonical {
		t.Fatalf("canonicalDeviceKey(variant) = %q, %v", got, ok)
	}
	if _, ok := canonicalDeviceKey("not a key"); ok {
		t.Fatal("opaque value treated as key")
	}

	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	d, err := s.AddDevice(t.Context(), NewDevice{Principal: "canon", Kind: DeviceKindWeb, Name: "Phone", Ed25519Public: variant}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if d.Ed25519Public != canonical {
		t.Fatalf("stored key %q, want canonical", d.Ed25519Public)
	}
	if _, err := s.RevokeDevice(t.Context(), "canon", d.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	found, err := s.DeviceByKey(t.Context(), variant)
	if err != nil || found.DeviceID != d.DeviceID || found.Active() {
		t.Fatalf("lookup by equivalent spelling: %+v, %v", found, err)
	}
	if _, err := s.AddDevice(t.Context(), NewDevice{Principal: "canon", Kind: DeviceKindWeb, Name: "Again", Ed25519Public: canonical}, 10); !errors.Is(err, ErrDeviceKeyInUse) {
		t.Fatalf("re-adding revoked key: %v", err)
	}
}

func TestAddDeviceRequiresAuthorizingPasskey(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddDevice(t.Context(), NewDevice{Principal: "pk", Kind: DeviceKindWeb, Name: "Phone", Ed25519Public: "pk-device",
		RequireCredentialID: []byte("deleted-credential")}, 10)
	if !errors.Is(err, ErrPasskeyNotFound) {
		t.Fatalf("err = %v, want ErrPasskeyNotFound", err)
	}
}

func TestPendingMessagesForFiltersBySession(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	sender := seedScratchUser(t, s, "pending-sender")
	recipient := seedScratchUser(t, s, "pending-recipient")
	if err := s.SpoolMessage(ctx, sender, recipient, "s", "account-wide"); err != nil {
		t.Fatal(err)
	}
	for i, device := range []string{"phone", "tablet", "phone"} {
		if err := s.SpoolMessage(ctx, sender, recipient, "s", fmt.Sprintf("copy-%d", i), DMKeyBinding{RecipientDeviceID: device}); err != nil {
			t.Fatal(err)
		}
	}
	bodies := func(f PendingFilter) []string {
		var out []string
		for {
			page, err := s.PendingMessagesFor(ctx, recipient, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range page {
				out = append(out, m.Message)
			}
			if len(page) < f.Limit {
				return out
			}
			f.AfterID = page[len(page)-1].ID
		}
	}
	if got := strings.Join(bodies(PendingFilter{DeviceID: "phone", Web: true, Limit: 1}), ","); got != "copy-0,copy-2" {
		t.Fatalf("web phone: %s", got)
	}
	if got := strings.Join(bodies(PendingFilter{Limit: 10}), ","); got != "account-wide" {
		t.Fatalf("desktop without device: %s", got)
	}
}
