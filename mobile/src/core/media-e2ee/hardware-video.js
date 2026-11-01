// Require runtime evidence from the actual RTP encoder, not only a local
// capability prediction. Unknown reports never authorize a media upload.
export function hardwareVideoEvidence(rows, trackID) {
    const video = [...rows.values()].filter(row => row.type === "outbound-rtp" &&
        !row.isRemote && row.active !== false && (row.kind || row.mediaType) === "video" &&
        !/video\/(rtx|red|ulpfec)/i.test(rows.get(row.codecId)?.mimeType || "") &&
        (!trackID || rows.get(row.mediaSourceId)?.trackIdentifier === trackID));
    if (video.length !== 1) return "pending";
    const row = video[0];
    if (row.powerEfficientEncoder === false || /libvpx|openh264|libaom|software|svt-av1/i.test(row.encoderImplementation || "")) return "software";
    // Efficiency is not a hardware guarantee in the stats standard. Require
    // an identified hardware backend too; generic ExternalEncoder is unknown.
    const hardware = /quicksync|\bqsv\b|\bmft\b|mediafoundation|d3d11|nvenc|videotoolbox|vaapi|va-api|v4l2|\bamf\b|hardware/i.test(row.encoderImplementation || "");
    return row.powerEfficientEncoder === true && hardware ? "hardware" : "pending";
}
