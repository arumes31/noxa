import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
const publications = new Map();
const uploads = new Map();

// A new capable server starts with no media upload until its own catalog
// confirms a viewer or recorder. Older servers keep continuous publication.
export async function preparePublicationUpload(slot, track, onChange) {
    const scope = captureMediaScope(), pc = V().state.pc;
    const supported = await window.go.main.App.SupportsStreamSourceQualityForTab?.(scope.tabID) || false;
    if (!mediaScopeIsCurrent(scope) || V().state.pc !== pc || track.readyState === "ended") return false;
    uploads.set(slot, { scope, pc, track, supported, active: !supported, generation: "0", onChange });
    return true;
}

function currentUpload(slot, track) {
    const upload = uploads.get(slot);
    return upload && upload.track === track && upload.track.readyState !== "ended" &&
        mediaScopeIsCurrent(upload.scope) && V().state.pc === upload.pc ? upload : null;
}

function applyUpload(upload, slot) {
    if (upload.applying) return upload.applying;
    const active = upload.active;
    upload.applying = Promise.resolve().then(() => upload.onChange?.()).then(() => {
        upload.applied = active;
    }).catch(error => {
        upload.applied = null;
        throw error;
    }).finally(() => {
        upload.applying = null;
        if (currentUpload(slot, upload.track) === upload && upload.active !== active) void applyUpload(upload, slot).catch(() => {});
    });
    return upload.applying;
}

export function publicationUploadActive(slot, track) {
    const upload = currentUpload(slot, track);
    return upload?.supported ? upload.active : undefined;
}

export function reconcilePublicationUploads(snapshot, streams) {
    for (const { publication, generation } of snapshot) {
        if (!valid(publication) || publication.generation !== generation) continue;
        const upload = currentUpload(publication.slot, publication.track);
        if (!upload?.supported || upload.generation !== generation) continue;
        const own = streams.find(stream => stream.publisher_id === publication.scope.clientID &&
            stream.slot === publication.slot && stream.generation === generation);
        if (typeof own?.upload_active !== "boolean" || own.upload_active === upload.active && upload.applied === upload.active) continue;
        upload.active = own.upload_active;
        void applyUpload(upload, publication.slot).catch(() => {});
    }
}

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
    const preparedUpload = currentUpload(slot, track);
    await stopPublication(slot, undefined, preparedUpload);
    if (!mediaScopeIsCurrent(scope) || V().state.pc !== pc || track.readyState === "ended") return false;
    if (preparedUpload && currentUpload(slot, track) !== preparedUpload) return false;
    const publication = { scope, pc, slot, track, onInvalidated, generation: "0", timer: null, video: null, cancelled: false };
    publications.set(slot, publication);
    try {
        const upload = currentUpload(slot, track);
        const result = await streamRequest(scope, { action: "publish", slot, active: true, ...(upload?.supported ? { quality_mode: "source" } : {}) });
        publication.generation = result.generation;
        if (!valid(publication)) {
            if (publications.get(slot) === publication) publications.delete(slot);
            await streamRequest(scope, { action: "publish", slot, generation: result.generation, active: false });
            return false;
        }
        if (upload && currentUpload(slot, track) === upload) {
            upload.generation = result.generation;
            if (upload.supported) {
                upload.active = result.upload_active === true;
                await applyUpload(upload, slot);
            }
        }
        if (!valid(publication)) return false;
        if (slot === "screen") void capturePreview(publication);
        return true;
    } catch (error) {
        if (publications.get(slot) === publication) {
            if (publication.generation !== "0") await stopPublication(slot, track).catch(() => {});
            else { publications.delete(slot); if (uploads.get(slot)?.track === track) uploads.delete(slot); }
        }
        throw error;
    }
}

export async function stopPublication(slot, track, preserveUpload = null) {
    const publication = publications.get(slot);
    const upload = uploads.get(slot);
    if (upload && upload !== preserveUpload && (!track || upload.track === track)) uploads.delete(slot);
    if (!publication || (track && publication.track !== track)) return;
    publications.delete(slot);
    publication.cancelled = true;
    clearTimeout(publication.timer);
    publication.cancelPlay?.();
    if (publication.video) { publication.video.pause(); publication.video.srcObject = null; }
    if (publication.generation !== "0") await streamRequest(publication.scope, { action: "publish", slot, generation: publication.generation, active: false });
}

export function stopPublications() {
    for (const slot of new Set([...publications.keys(), ...uploads.keys()])) void stopPublication(slot).catch(() => {});
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
