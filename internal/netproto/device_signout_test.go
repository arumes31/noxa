package netproto

import "testing"

func TestSignOutCannotNameAnotherDevice(t *testing.T) {
	if !(DeviceRequest{Action: DeviceActionSignOut}).Valid() {
		t.Fatal("self signout rejected")
	}
	if (DeviceRequest{Action: DeviceActionSignOut, DeviceID: "master"}).Valid() {
		t.Fatal("signout may not choose a target")
	}
}
