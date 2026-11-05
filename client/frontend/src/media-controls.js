import { channelMedia, mediaErrorText, MediaSessionError } from "./channel-media.js";

const V = () => window.__noxa;
let whisperRequest = null;
let whisperRouting = null;

// Whisper channel targets are channel IDs of one server. They are applied
// only on the server they were chosen on: the same ID names an unrelated
// channel elsewhere, and routing voice there would leak it.
export function whisperChannelsFor(settings, server) {
    const channels = settings?.whisper_channels || [];
    return server && settings?.whisper_channels_server === server ? [...channels] : [];
}

// bindWhisperChannels makes the channel list belong to server before it is
// edited there; a list chosen on another server (or unbound) starts empty.
export function bindWhisperChannels(settings, server) {
    if (settings.whisper_channels_server !== server) {
        settings.whisper_channels = [];
        settings.whisper_channels_server = server || "";
    }
    settings.whisper_channels = settings.whisper_channels || [];
    return settings.whisper_channels;
}

export function currentWhisperRouting() {
    return whisperRouting && mediaScopeIsCurrent(whisperRouting.scope) ? whisperRouting : null;
}

export function captureMediaScope() {
    const s = V().state;
    return { tabID: s.activeTabID, generation: s.serverGeneration, session: s.sessionGeneration, clientID: s.myClientID, channelID: s.myChannelID };
}

export function mediaScopeIsCurrent(scope) {
    const current = captureMediaScope();
    return Object.keys(current).every(key => current[key] === scope[key]);
}

// Null means obsolete; empty string confirms success; a string reports failure.
// Shared ownership prevents a hotkey completion from overwriting newer settings.
export async function setWhisperRouting(config, scope = captureMediaScope()) {
    if (!scope.tabID || !scope.clientID || !mediaScopeIsCurrent(scope)) return null;
    // A whisper routes the microphone outside the channel key scope: it is
    // unavailable while channel media is end-to-end encrypted.
    if (config.active && channelMedia()) return mediaErrorText(new MediaSessionError("whisper_unavailable"));
    const request = { scope, config: structuredClone(config) };
    whisperRequest = request;
    whisperRouting = { scope, status: "pending" };
    V().renderVoiceStatus?.();
    let error;
    try {
        error = await window.go.main.App.WhisperSetForTab(scope.tabID, config.clients, config.channels, config.active);
    } catch (err) { error = String(err); }
    if (whisperRequest !== request || !mediaScopeIsCurrent(scope)) return null;
    whisperRequest = null;
    whisperRouting = error ? { scope, status: "failed" } : { scope, status: "confirmed", config: request.config };
    V().renderVoiceStatus?.();
    return error || "";
}
