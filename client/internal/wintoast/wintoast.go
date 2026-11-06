// Package wintoast shows Windows toast notifications (Action Center) from an
// unpackaged desktop app and receives their activations, including text typed
// into an inline input, in pure Go without cgo.
//
// The COM activator (IClassFactory / INotificationActivationCallback) and the
// registry layout are derived from git.sr.ht/~jackmordaunt/go-toast/v2
// v2.0.3 (SPDX-License-Identifier: Unlicense OR MIT; see LICENSE in this
// directory). This copy adds what noXa needs and the upstream API lacks:
// toast tags and groups (so a conversation's toast replaces the previous one
// and can be removed when the conversation is read), history removal, a
// dedicated COM thread, and correct `this` pointers for WinRT factory calls.
package wintoast

import "errors"

// ErrUnsupported is returned on platforms without Windows toasts.
var ErrUnsupported = errors.New("windows toast notifications are not supported on this platform")

// AppData identifies the application to Windows.
type AppData struct {
	AppID       string // AppUserModelID, e.g. "noXa.Desktop"
	DisplayName string // shown in Action Center and Settings
	CLSID       string // activator class GUID, "{XXXXXXXX-...}"
	Executable  string // started by COM ("-Embedding") when the app is not running
	IconPath    string // optional PNG/ICO file
}

// UserData is one input value of an activated toast (input id -> text).
type UserData struct {
	Key   string
	Value string
}

// Callback receives activations. It runs on a COM thread: it must return
// quickly and never call back into this package synchronously.
type Callback func(appID, arguments string, inputs []UserData)

// Toast is one notification: its XML and the tag/group that identify it.
type Toast struct {
	XML   string
	Tag   string // up to 64 characters
	Group string // up to 64 characters
}
