// external-url.js: the only way the frontend opens a link in the system browser.
// The native OpenExternalURL binding accepts http(s) URLs with a host only, so
// webview content can never hand the shell another kind of target.

/** Opens url externally; rejects when the native side refuses or fails. */
export async function openExternalURL(url) {
    const error = await window.go.main.App.OpenExternalURL(String(url ?? ""));
    if (error) throw new Error(error);
}
