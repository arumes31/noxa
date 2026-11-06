# wintoast — origin and changes

Derived from `git.sr.ht/~jackmordaunt/go-toast/v2` v2.0.3
(`SPDX-License-Identifier: Unlicense OR MIT`, see `LICENSE`).

Kept from upstream: the pure-Go `IClassFactory` / `INotificationActivationCallback`
COM objects and the per-user registry layout (`HKCU\Software\Classes\AppUserModelId\<id>`
with `CustomActivator`, `HKCU\Software\Classes\CLSID\{clsid}\LocalServer32`).

Changed for noXa (item 501):

- Toast tag and group (`IToastNotification2`) so a conversation's toast replaces
  the previous one, plus `ToastNotificationHistory` removal (`RemoveGroupedTagWithId`,
  `ClearWithId`). Vtable slots follow the Windows SDK 10.0.26100 headers.
- All WinRT calls run on one dedicated OS thread in the multithreaded apartment;
  the activator class is registered there for the whole process at startup, so
  activations of toasts shown before reach the running app.
- WinRT factory methods receive their factory as `this` (upstream passed 0).
- The activation callback copies its strings and runs the handler on its own
  goroutine; input data is capped at 16 entries.
- No PowerShell fallback and no templating: callers pass finished XML.

`manual_windows_test.go` (build tag `manualtoast`) shows, replaces and removes a
real toast on a Windows desktop.
