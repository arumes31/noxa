const fingerprintPattern = /presented:\s*((?:[0-9a-f]{2}:){31}[0-9a-f]{2})/i;

export function extractPresentedFingerprint(detail) {
    if (typeof detail !== "string") return "";
    return detail.match(fingerprintPattern)?.[1]?.toLowerCase() || "";
}

// A file or link dropped outside the app's own drop zones must never be
// opened by the WebView in place of the app. The listeners run in the bubble
// phase, after any drop zone under the pointer has handled the event. A file
// chooser keeps its own drop: it takes the file and never opens it.
export function installDropGuard(target) {
    const chooser = (event) => event.target?.type === "file";
    target.addEventListener("dragover", (event) => {
        if (event.defaultPrevented || chooser(event)) return; // a drop zone accepts this drag
        event.preventDefault();
        if (event.dataTransfer) event.dataTransfer.dropEffect = "none";
    });
    target.addEventListener("drop", (event) => { if (!chooser(event)) event.preventDefault(); });
}
