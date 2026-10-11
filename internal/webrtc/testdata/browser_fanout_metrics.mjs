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
