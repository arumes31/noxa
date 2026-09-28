export const PLAYBACK_SPEEDS = [.5, .75, 1, 1.25, 1.5, 2];
export function waveformPeaks(channels, bars = 64) {
    const peaks = new Array(bars).fill(0);
    const length = channels[0]?.length || 0;
    for (let bar = 0; bar < bars; bar++) {
        const start = Math.floor(bar * length / bars), end = Math.floor((bar + 1) * length / bars);
        for (const channel of channels) for (let i = start; i < end; i++) {
            const value = Math.abs(channel[i]); if (Number.isFinite(value)) peaks[bar] = Math.min(1, Math.max(peaks[bar], value));
        }
    }
    return peaks;
}
export function boundedPlaybackHistory(entries, now = Date.now()) {
    if (!Array.isArray(entries)) return [];
    return entries.filter(e => e && typeof e.key === 'string' && e.key.length <= 128 && Number.isFinite(e.position) && e.position >= 0 && e.position <= 7200 && PLAYBACK_SPEEDS.includes(e.speed) && Number.isFinite(e.at) && e.at <= now && now-e.at < 30*86400000)
        .sort((a,b) => b.at-a.at).slice(0,100);
}
