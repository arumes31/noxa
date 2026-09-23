import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
const publications = new Map();

// Ignore queued notifications after a stop, replacement, or connection change.
export function isCurrentPublication(data) {
    const publication = publications.get(data.slot);
    return !!publication && valid(publication) && publication.generation !== "0" &&
        data.publisher_id === publication.scope.clientID && data.generation === publication.generation;
}

export function streamRequest(scope, body) {
    return window.go.main.App.VideoStreamControlForTab(scope.tabID, {
        publisher_id: "", slot: "", generation: "0", revision: "0", session: "0", active: false, ...body,
    });
}

export async function startPublication(slot, track, onInvalidated = () => track.stop()) {
    const scope = captureMediaScope();
    const pc = V().state.pc;
    await stopPublication(slot);
    if (!mediaScopeIsCurrent(scope) || V().state.pc !== pc || track.readyState === "ended") return false;
    const publication = { scope, pc, slot, track, onInvalidated, generation: "0", timer: null, video: null, cancelled: false };
    publications.set(slot, publication);
    try {
        const result = await streamRequest(scope, { action: "publish", slot, active: true });
        publication.generation = result.generation;
        if (!valid(publication)) {
            if (publications.get(slot) === publication) publications.delete(slot);
            await streamRequest(scope, { action: "publish", slot, generation: result.generation, active: false });
            return false;
        }
        if (slot === "screen") void capturePreview(publication);
        return true;
    } catch (error) {
        if (publications.get(slot) === publication) publications.delete(slot);
        throw error;
    }
}

export async function stopPublication(slot, track) {
    const publication = publications.get(slot);
    if (!publication || (track && publication.track !== track)) return;
    publications.delete(slot);
    publication.cancelled = true;
    clearTimeout(publication.timer);
    publication.cancelPlay?.();
    if (publication.video) { publication.video.pause(); publication.video.srcObject = null; }
    if (publication.generation !== "0") await streamRequest(publication.scope, { action: "publish", slot, generation: publication.generation, active: false });
}

export function stopPublications() {
    for (const slot of publications.keys()) void stopPublication(slot).catch(() => {});
}

// Freeze only acknowledged generations that existed before the catalog request.
// A publication accepted while that request is pending belongs to the next poll.
export function publicationSnapshot() {
    return [...publications.values()].filter(p => p.generation !== "0").map(p => ({ publication: p, generation: p.generation }));
}

export function reconcilePublications(snapshot, streams) {
    for (const { publication, generation } of snapshot) {
        if (!valid(publication) || publication.generation !== generation) continue;
        if (streams.some(s => s.publisher_id === publication.scope.clientID && s.slot === publication.slot && s.generation === generation)) continue;
        publication.generation = "0";
        void stopPublication(publication.slot).catch(() => {});
        publication.onInvalidated();
        V().sysMsg(t("streams.publicationEnded"));
    }
}

function valid(publication) {
    return publications.get(publication.slot) === publication && !publication.cancelled && mediaScopeIsCurrent(publication.scope) && V().state.pc === publication.pc && publication.track.readyState !== "ended";
}

async function capturePreview(publication) {
    if (!valid(publication)) return;
    try {
        const video = document.createElement("video");
        publication.video = video;
        video.muted = true;
        video.playsInline = true;
        video.srcObject = new MediaStream([publication.track]);
        await new Promise((resolve, reject) => {
            const finish = error => {
                clearTimeout(deadline);
                publication.cancelPlay = null;
                if (error) reject(error); else resolve();
            };
            const deadline = setTimeout(() => finish(new Error("preview frame timed out")), 5000);
            publication.cancelPlay = () => finish(new Error("preview cancelled"));
            video.play().then(() => finish(), finish);
        });
        if (!valid(publication)) return;
        const width = video.videoWidth, height = video.videoHeight;
        if (!width || !height) throw new Error("preview frame unavailable");
        const scale = Math.min(1, 640 / width, 360 / height);
        const canvas = document.createElement("canvas");
        canvas.width = Math.max(1, Math.floor(width * scale));
        canvas.height = Math.max(1, Math.floor(height * scale));
        canvas.getContext("2d").drawImage(video, 0, 0, canvas.width, canvas.height);
        const url = canvas.toDataURL("image/jpeg", 0.55);
        const jpeg = url.slice(url.indexOf(",") + 1);
        if (!valid(publication) || jpeg.length > 65536) return;
        await streamRequest(publication.scope, { action: "preview_upload", slot: "screen", generation: publication.generation, jpeg });
    } catch (error) {
        if (valid(publication)) V().sysMsg(t("streams.previewFailed", { error: String(error) }));
    } finally {
        if (publication.video) { publication.video.pause(); publication.video.srcObject = null; publication.video = null; }
        if (valid(publication)) publication.timer = setTimeout(() => { void capturePreview(publication); }, 120000);
    }
}
