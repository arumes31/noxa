const fingerprintPattern = /presented:\s*((?:[0-9a-f]{2}:){31}[0-9a-f]{2})/i;

export function extractPresentedFingerprint(detail) {
    if (typeof detail !== "string") return "";
    return detail.match(fingerprintPattern)?.[1]?.toLowerCase() || "";
}

// A file or link dropped outside the app's own drop zones must never be
// opened by the WebView in place of the app. The listeners run in the bubble
// phase, after any drop zone under the pointer has handled the event. Two
// drops keep their default, because neither opens anything: a file chooser
// taking a file, and a text field taking dragged text.
export function installDropGuard(target) {
    const own = (event) => {
        const t = event.target;
        if (t?.type === "file") return true;
        const field = t?.isContentEditable || t?.tagName === "TEXTAREA" || t?.tagName === "INPUT";
        return Boolean(field) && !Array.from(event.dataTransfer?.types || []).includes("Files");
    };
    target.addEventListener("dragover", (event) => {
        if (event.defaultPrevented || own(event)) return; // a drop zone accepts this drag
        event.preventDefault();
        if (event.dataTransfer) event.dataTransfer.dropEffect = "none";
    });
    target.addEventListener("drop", (event) => { if (!own(event)) event.preventDefault(); });
}
