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
    return row.powerEfficientEncoder === true ? "hardware" : "pending";
}
