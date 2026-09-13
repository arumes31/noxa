# Changelog

## 0.4.1

### Fixed
- Check for newer GitHub releases when the application starts, before connecting to a server, and offer an Update now button.
- Show download progress and retryable download/restart errors; retain Restart now after closing and reopening the update dialog.
- Exit the old client during an update restart even when close-to-tray is enabled.

### Validation
- Add signed-download and Windows executable replacement integration tests to the release CI gate.