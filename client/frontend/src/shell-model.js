// shell-model.js — pure helpers for the app shell (no DOM, unit tested).

// Every visible channel has one chat. Voice entries here are links to that
// same channel, not duplicated server channels or independent subscriptions.
export function textChannels(channels = []) {
    const position = new Map(channels.map((channel, index) => [channel.ChannelID, index]));
    return channels.filter((channel) => channel.ChannelID > 0 && channel.SystemKind !== "home")
        .sort((a, b) => (a.OrderIndex || 0) - (b.OrderIndex || 0) || position.get(a.ChannelID) - position.get(b.ChannelID));
}

// formatStreamStats renders "1080p60 · 6.2 Mbps" from a share sample.
export function formatStreamStats(stats) {
    if (!stats) return "";
    const height = Number(stats.height) > 0 ? Math.round(stats.height) : 0;
    const fps = Number.isFinite(stats.fps) && stats.fps > 0 ? Math.round(stats.fps) : 0;
    const resolution = height ? `${height}p${fps || ""}` : "";
    const bitrate = Number.isFinite(stats.bitrate) && stats.bitrate > 0
        ? stats.bitrate >= 1e6 ? `${(stats.bitrate / 1e6).toFixed(1)} Mbps` : `${Math.round(stats.bitrate / 1000)} kbps`
        : "";
    return [resolution, bitrate].filter(Boolean).join(" · ");
}

export function serverInitial(name = "") {
    const first = [...String(name).replace(/^[^\p{L}\p{N}]+/u, "")][0] || "";
    return first.toLocaleUpperCase();
}

// Settings pages the quick switcher can open (id, label key). The settings
// dialog owns the real page list; an unknown id opens its first page.
export const QUICK_SWITCH_SETTINGS = [
    ["application", "settings.application"], ["capture", "settings.capture"], ["camera", "settings.camera"],
    ["playback", "settings.playback"], ["overlay", "settings.overlay"], ["hotkeys", "settings.hotkeys"],
    ["whisper", "settings.whisper"], ["downloads", "settings.downloads"], ["chat", "settings.chat"],
    ["security", "settings.security"], ["server", "settings.server"], ["notifications", "settings.notifications"],
];
