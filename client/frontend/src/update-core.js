// Eighty visual blocks divide the measured payload; they are not network packets.
export function measuredUpdateProgress(percent, details) {
    const bytes = details?.downloaded_bytes;
    const total = details?.total_bytes;
    const downloaded = Number.isSafeInteger(bytes) && bytes >= 0 ? bytes : null;
    const size = Number.isSafeInteger(total) && total > 0 ? total : null;
    const ratio = downloaded !== null && size !== null ? Math.min(1, downloaded / size)
        : Number.isFinite(percent) && percent >= 0 ? Math.min(1, percent / 100) : null;
    return { downloaded, total: size, ratio, blocks: ratio === null ? null : Math.floor(ratio * 80) };
}
