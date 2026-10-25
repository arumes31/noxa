package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPushSubscriptionsFollowTheirDevice(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("push-principal-%d", time.Now().UnixNano())
	first, err := s.AddDevice(ctx, newTestDevice(principal, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := s.AddDevice(ctx, newTestDevice(principal, 1), 10)
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.AddDevice(ctx, newTestDevice(principal, 2), 10)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://push.example/" + principal
	sub := PushSubscription{DeviceID: phone.DeviceID, Principal: principal, Endpoint: endpoint, P256dh: []byte{4, 1}, Auth: []byte("0123456789abcdef"), HideDetails: true}
	if err := s.SavePushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePushSubscription(ctx, PushSubscription{DeviceID: phone.DeviceID, Principal: "someone-else", Endpoint: endpoint + "/x", P256dh: []byte{4}, Auth: []byte("a")}); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("subscription for another account's device err = %v", err)
	}
	got, err := s.PushSubscriptionForDevice(ctx, principal, phone.DeviceID)
	if err != nil || !got.HideDetails || got.AlwaysNotify || got.Endpoint != endpoint {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if err := s.SetPushPreferences(ctx, principal, phone.DeviceID, false, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PushSubscriptionForDevice(ctx, principal, phone.DeviceID); got.HideDetails || !got.AlwaysNotify {
		t.Fatalf("preferences = %+v", got)
	}
	if got, _ := s.PushSubscriptionForDevice(ctx, principal, phone.DeviceID); string(got.Filters) != "{}" {
		t.Fatalf("default filters = %q", got.Filters)
	}
	if err := s.SetPushFilters(ctx, principal, phone.DeviceID, []byte(`{"chat_level":"dm"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPushFilters(ctx, "someone-else", phone.DeviceID, []byte(`{}`)); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("filters for another account's device err = %v", err)
	}
	// Re-subscribing (browsers rotate endpoints) keeps the filters.
	if err := s.SavePushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PushSubscriptionForDevice(ctx, principal, phone.DeviceID); string(got.Filters) != `{"chat_level": "dm"}` {
		t.Fatalf("filters = %q", got.Filters)
	}

	// The same browser endpoint reported by another device moves there.
	moved := sub
	moved.DeviceID = other.DeviceID
	if err := s.SavePushSubscription(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushSubscriptionForDevice(ctx, principal, phone.DeviceID); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("endpoint stayed on the old device: %v", err)
	}

	// Failures accumulate and drop the subscription; success resets them.
	for i := 0; i < 2; i++ {
		if err := s.RecordPushResult(ctx, endpoint, false, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordPushResult(ctx, endpoint, true, 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.PushSubscriptionForDevice(ctx, principal, other.DeviceID); got.Failures != 0 {
		t.Fatalf("failures after success = %d", got.Failures)
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordPushResult(ctx, endpoint, false, 3); err != nil {
			t.Fatal(err)
		}
	}
	if subs, _ := s.PushSubscriptions(ctx, principal); len(subs) != 0 {
		t.Fatalf("failing subscription kept: %+v", subs)
	}

	// Revoking a device drops its subscription.
	sub.Endpoint = endpoint + "/2"
	if err := s.SavePushSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDevice(ctx, principal, phone.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	if subs, _ := s.PushSubscriptions(ctx, principal); len(subs) != 0 {
		t.Fatalf("revoked device still subscribed: %+v", subs)
	}
	_ = first
}
