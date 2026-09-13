# Changelog

## 0.4.2

### Fixed
- Reject signed update payloads that are not Windows AMD64 executables before replacing the installed client.
- Validate PE headers, executable sections, and the entry point to reject incorrectly packaged or truncated binaries.

### Validation
- Exercise signed wrong-platform, architecture, DLL, malformed-header, and truncated-section payloads through the real updater with disposable executable copies.

## 0.4.1

### Fixed
- Check for newer GitHub releases when the application starts, before connecting to a server, and offer an Update now button.
- Show download progress and retryable download/restart errors; retain Restart now after closing and reopening the update dialog.
- Exit the old client during an update restart even when close-to-tray is enabled.

### Validation
- Add signed-download and Windows executable replacement integration tests to the release CI gate.