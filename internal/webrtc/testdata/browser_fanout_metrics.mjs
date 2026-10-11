// A positive endpoint difference cannot establish continuity: a reset midway
// through a stage can catch up before its final sample. Check every sample and
// keep receiver/observer identities separate from rolling rVFC timing windows.
export function counterDelta(rows, field, identities = []) {
    if (rows.length < 2) return null;
    const first = rows[0];
    let previous;
    for (const row of rows) {
        const value = row?.[field];
        if (!Number.isFinite(value) || value < 0 || (previous !== undefined && value < previous) ||
            identities.some(key => row?.[key] == null || row[key] !== first?.[key])) return null;
        previous = value;
    }
    return previous - first[field];
}

export function counterRate(delta, seconds) {
    return delta !== null && Number.isFinite(delta) && delta >= 0 && Number.isFinite(seconds) && seconds > 0 ? delta / seconds : null;
}

export function sourceFrameTimingSummary(rows, seconds) {
    if (!rows.length || rows.some(row => !row)) return null;
    const change = field => counterDelta(rows, field, ['epoch']);
    const opportunities = change('raf_opportunities'), skips = change('throttle_skips');
    const draws = change('completed_draws'), duration = change('sync_draw_ms');
    if ([opportunities, skips, draws, duration].includes(null)) return null;
    const bins = field => {
        const length = rows[0][field]?.length;
        if (!length || rows.some(row => row[field]?.length !== length)) return null;
        const result = Array.from({ length }, (_, index) => counterDelta(rows.map(row => ({ epoch: row.epoch, value: row[field][index] })), 'value', ['epoch']));
        return result.includes(null) ? null : result;
    };
    return { epoch: rows[0].epoch, raf_opportunity_fps: counterRate(opportunities, seconds),
        throttle_skip_fps: counterRate(skips, seconds), throttle_skip_ratio: opportunities > 0 ? skips / opportunities : null,
        completed_draw_fps: counterRate(draws, seconds), mean_sync_draw_ms: draws > 0 ? duration / draws : null,
        sync_draw_bins: bins('sync_draw_bins'), raf_gap_bins: bins('raf_gap_bins') };
}
