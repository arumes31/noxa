import { playClosingAnnouncement } from "./sounds.js";

// The native close hook keeps the WebView alive until playback completes.
export function initClosingAudio({ runtime = window.runtime, app = window.go.main.App, play = playClosingAnnouncement } = {}) {
    let closing;
    runtime.EventsOn("app_closing", () => {
        closing ||= Promise.resolve().then(play).catch(() => {})
            .then(() => app.CompleteClose()).catch(() => {});
        return closing;
    });
    // Register only after the listener exists. Native shutdown has its own
    // timeout if the renderer or bridge stops responding.
    void Promise.resolve(app.ReadyForCloseNotifications()).catch(() => {});
}
