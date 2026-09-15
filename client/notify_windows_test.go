package main

import "testing"

func TestNativeNotificationIsSilent(t *testing.T) {
	data := silentNotificationData(1, "noXa", "A message")
	if data.InfoFlags&notificationNoSound == 0 {
		t.Fatal("native notifications must not add a system sound")
	}
	if data.Title[0] != 'n' || data.Info[0] != 'A' {
		t.Fatal("silent notification lost its visual content")
	}
}
