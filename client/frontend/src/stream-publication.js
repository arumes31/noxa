import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { t } from "./i18n.js";
import { hevcReadmissionDelay } from "./video-hevc.js";
import { publicationCodecSupport, publicationCodecQueryKey } from "./video-codecs.js";

const V = () => window.__noxa;
const publications = new Map();
const uploads = new Map();
const codecRetryDelays = [30000, 120000, 300000];

async function supportsCodecRefresh(tabID) {
    let timer;
    try {
        return await Promise.race([
            Promise.resolve().then(() => window.go.main.App.SupportsStreamCodecRefreshForTab?.(tabID)).catch(() => false),
            new Promise(resolve => { timer = setTimeout(() => resolve(false), 1000); }),
        ]) === true;
    } finally { clearTimeout(timer); }
}

// A new capable server starts with no media upload until its own catalog
// confirms a viewer or recorder. Older servers keep continuous publication.
export async function preparePublicationUpload(slot, track, onChange) {
    const scope = captureMediaScope(), pc = V().state.pc;
    const supported = await window.go.main.App.SupportsStreamSourceQualityForTab?.(scope.tabID) || false;
    const codecSupport = supported && await window.go.main.App.SupportsStreamCodecForTab?.(scope.tabID) || false;
    const hevc = codecSupport && await window.go.main.App.SupportsStreamHEVCForTab?.(scope.tabID) === true;
    const probe = codecSupport ? await publicationCodecSupport(track, hevc) : null;
    const codecs = probe?.codecs;
    const refreshSupported = codecSupport && await supportsCodecRefresh(scope.tabID);
    if (!mediaScopeIsCurrent(scope) || V().state.pc !== pc || track.readyState === "ended") return false;
    uploads.set(slot, { scope, pc, track, supported, codecs, hevc, codecRevision: hevc ? 1n : 0n, hevcLevel: probe?.hevc_level_id, hevcFormat: probe?.hevcFormat, codec: codecs?.[0], active: !supported, generation: "0", onChange,
        refresh: { supported: refreshSupported, done: hevc ? probe?.preferredAvailable === true : codecs?.includes("h264"), pending: false, key: probe?.key,
            last: Date.now(), next: Date.now() + (probe?.status === "unsupported" ? 300000 : 30000), attempts: 0 } });
    return true;
}

function currentUpload(slot, track) {
    const upload = uploads.get(slot);
    return upload && upload.track === track && upload.track.readyState !== "ended" &&
        mediaScopeIsCurrent(upload.scope) && V().state.pc === upload.pc ? upload : null;
}

function applyUpload(upload, slot, demandOnly = false) {
    if (upload.applying) {
        if (demandOnly) upload.demandOnlyPending = true;
        return upload.applying;
    }
    const active = upload.active;
    const codec = upload.codec;
    const format = upload.hevcFormat;
    upload.applying = Promise.resolve().then(() => upload.onChange?.({ demandOnly })).then(() => {
        upload.applied = active;
        if (!demandOnly) { upload.appliedCodec = codec; upload.appliedFormat = format; }
    }).catch(error => {
        upload.applied = null;
        throw error;
    }).finally(() => {
        upload.applying = null;
        const queuedDemandOnly = upload.demandOnlyPending;
        const nextDemandOnly = queuedDemandOnly || demandOnly;
        upload.demandOnlyPending = false;
        if (currentUpload(slot, upload.track) === upload && (upload.active !== active || queuedDemandOnly && upload.applied !== upload.active ||
            !nextDemandOnly && (upload.codec !== codec || upload.hevcFormat !== format))) void applyUpload(upload, slot, nextDemandOnly).catch(() => {});
    });
    return upload.applying;
}

export function publicationUploadActive(slot, track) {
    const upload = currentUpload(slot, track);
    return upload?.supported ? upload.active : undefined;
}

export function publicationCodec(slot, track) { return currentUpload(slot, track)?.codec; }
export function publicationHEVCFormat(slot, track) { return currentUpload(slot, track)?.hevcFormat; }
function codecFields(upload) {
    return upload?.codecs ? { codecs: upload.codecs, ...(upload.hevc ? { codec_revision: String(upload.codecRevision) } : {}), ...(upload.codecs.includes("h265") ? { hevc_level_id: upload.hevcLevel } : {}) } : {};
}
function selectedCodec(upload, value, level) {
    if (value === "h265" && level !== upload.hevcLevel) throw new Error("HEVC publication level changed");
    return ["h265", "h264"].includes(value) && upload.codecs?.includes(value) ? value : "vp8";
}

// Versioned capability replacement is ordered per publication. A delayed
// hardware result or lost reply cannot re-enable a format withdrawn later.
async function replaceCapabilities(publication, upload, result, revision, stillCurrent = () => true) {
    const slot = publication.slot, generation = publication.generation;
    const current = () => valid(publication) && currentUpload(slot, upload.track) === upload &&
        generation !== "0" && publication.generation === generation && upload.codecRevision === revision && stillCurrent();
    if (!current()) return false;
    upload.codecs = result.codecs; upload.hevcLevel = result.hevc_level_id; upload.hevcFormat = result.hevcFormat;
    const fields = codecFields(upload);
    upload.pendingCapabilities = { result, revision };
    upload.capabilityRetryAt = Date.now() + 30000;
    const previous = upload.capabilityPending || Promise.resolve();
    const pending = previous.catch(() => {}).then(async () => {
        if (!current()) return false;
        const answer = await streamRequest(upload.scope, { action: "publish", slot, generation, active: true, quality_mode: "source", ...fields });
        if (!current()) return false;
        if (upload.hevc && String(answer.codec_revision) !== String(revision)) throw new Error("Video codec revision changed");
        if (upload.hevc) upload.codec = selectedCodec(upload, answer.codec, answer.hevc_level_id);
        upload.pendingCapabilities = null;
        return true;
    });
    upload.capabilityPending = pending;
    try { return await pending; } finally { if (upload.capabilityPending === pending) upload.capabilityPending = null; }
}

// Called only after an actual sustained known-software HEVC observation or a
// failed HEVC preference/negotiation. Keep the source, generation and one sender.
export async function withdrawPublicationHEVC(slot, track, stillCurrent = () => true, apply = true) {
    const upload = currentUpload(slot, track), publication = publications.get(slot);
    if (!upload?.hevc || !upload.codecs?.includes("h265") || !stillCurrent()) return false;
    const prepared = upload.generation === "0" && !valid(publication);
    if (!prepared && !valid(publication)) return false;
    const previousRevision = upload.codecRevision;
    const revision = prepared ? upload.codecRevision : ++upload.codecRevision;
    const delay = hevcReadmissionDelay(upload.refresh.readmissions || 0);
    upload.refresh.withdrawn = true;
    upload.refresh.suppressHEVCUntil = delay === null ? Infinity : Date.now() + delay;
    upload.refresh.next = upload.refresh.suppressHEVCUntil; upload.refresh.done = delay === null;
    const result = await publicationCodecSupport(track, false);
    if (currentUpload(slot, track) !== upload || upload.codecRevision !== revision || !stillCurrent()) {
        // No control request was sent. Retire only our uncommitted reservation,
        // never a newer withdrawal/augmentation owned by another operation.
        if (upload.codecRevision === revision) upload.codecRevision = previousRevision;
        return false;
    }
    if (prepared) {
        if (upload.generation !== "0" || valid(publications.get(slot))) return false;
        upload.codecs = result.codecs; upload.hevcLevel = undefined; upload.hevcFormat = undefined; upload.codec = result.codecs[0];
        return true;
    }
    if (!valid(publication)) return false;
    const replaced = await replaceCapabilities(publication, upload, result, revision, stillCurrent);
    if (replaced && apply) await applyUpload(upload, slot);
    return replaced;
}

// Retry the same exact pending replacement after a rejected/lost request. An
// older catalog is not authority to undo a newer local hardware withdrawal.
function retryCapabilities(publication, upload) {
    const pending = upload.pendingCapabilities;
    if (!pending || upload.capabilityPending || Date.now() < upload.capabilityRetryAt) return;
    void replaceCapabilities(publication, upload, pending.result, pending.revision).then(applied => {
        if (applied && currentUpload(publication.slot, upload.track) === upload) return applyUpload(upload, publication.slot);
    }).catch(() => {});
}

// Catalog polling drives retries without waiting for a native capability query
// or publishing again. Only the server selects the active shared codec.
function refreshPublicationCodec(publication, upload) {
    const retry = upload.refresh, slot = publication.slot, generation = publication.generation;
    if (!retry?.supported || retry.done || retry.pending || upload.pendingCapabilities || upload.active !== true || Date.now() < (retry.suppressHEVCUntil || 0)) return;
    const key = publicationCodecQueryKey(upload.track, upload.hevc), now = Date.now();
    if (key === null || (now < retry.next && (key === retry.key || now - retry.last < 30000))) return;
    const revisionBefore = upload.codecRevision;
    const current = () => upload.codecRevision === revisionBefore && valid(publication) && publication.generation === generation && generation !== "0" &&
        currentUpload(slot, upload.track) === upload && upload.generation === generation;
    if (!current()) return;
    retry.pending = true; retry.key = key; retry.last = now;
    retry.next = now + codecRetryDelays[Math.min(retry.attempts++, codecRetryDelays.length - 1)];
    void (async () => {
        try {
            const result = await publicationCodecSupport(upload.track, upload.hevc);
            if (!current() || publicationCodecQueryKey(upload.track, upload.hevc) !== key) return;
            if (!result.codecs.some(codec => codec !== "vp8") || (!retry.advertising && !result.codecs.some(codec => codec !== "vp8" && !upload.codecs.includes(codec)) && result.hevcFormat?.sdpFmtpLine === upload.hevcFormat?.sdpFmtpLine)) {
                if (result.status === "unsupported") retry.next = Math.max(retry.next, Date.now() + 300000);
                return;
            }
            // Keep locally proven support even if the acknowledgement is lost:
            // a later authoritative catalog may show the accepted augmentation.
            // Never select H264 merely because the capability query succeeded.
            if (upload.hevc && retry.withdrawn && result.codecs.includes("h265")) {
                if (hevcReadmissionDelay(retry.readmissions || 0) === null) { retry.done = true; return; }
                retry.readmissions = (retry.readmissions || 0) + 1;
            }
            const revision = upload.hevc ? ++upload.codecRevision : upload.codecRevision;
            retry.advertising = true;
            const capabilities = upload.hevc ? result : { ...result, codecs: [...new Set([...result.codecs, ...upload.codecs])] };
            if (await replaceCapabilities(publication, upload, capabilities, revision)) {
                retry.advertising = false; retry.done = upload.hevc ? result.preferredAvailable === true : true;
            }
        } catch {
            // Older/rejecting servers and lost replies cannot cause poll-rate
            // mutations. The next exact-generation request is idempotent.
        } finally { retry.pending = false; }
    })();
}

export function reconcilePublicationUploads(snapshot, streams) {
    for (const { publication, generation } of snapshot) {
        if (!valid(publication) || publication.generation !== generation) continue;
        const upload = currentUpload(publication.slot, publication.track);
        if (!upload?.supported || upload.generation !== generation) continue;
        const own = streams.find(stream => stream.publisher_id === publication.scope.clientID &&
            stream.slot === publication.slot && stream.generation === generation);
        retryCapabilities(publication, upload);
        if (typeof own?.upload_active !== "boolean") continue;
        if (upload.hevc && String(own.codec_revision) !== String(upload.codecRevision)) {
            // Demand is authoritative for this exact generation even while its
            // codec replacement is pending. Never adopt the older codec here.
            upload.active = own.upload_active;
            if (upload.applied !== upload.active) void applyUpload(upload, publication.slot, true).catch(() => {});
            continue;
        }
        upload.pendingCapabilities = null;
        refreshPublicationCodec(publication, upload);
        const codec = upload.codecs ? selectedCodec(upload, own.codec, own.hevc_level_id) : undefined;
        if (own.upload_active === upload.active && upload.applied === upload.active && codec === upload.codec && codec === upload.appliedCodec && upload.hevcFormat === upload.appliedFormat) continue;
        upload.codec = codec;
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
        const result = await streamRequest(scope, { action: "publish", slot, active: true, ...(upload?.supported ? { quality_mode: "source" } : {}), ...codecFields(upload) });
        publication.generation = result.generation;
        if (!valid(publication)) {
            if (publications.get(slot) === publication) publications.delete(slot);
            await streamRequest(scope, { action: "publish", slot, generation: result.generation, active: false });
            return false;
        }
        if (upload && currentUpload(slot, track) === upload) {
            upload.generation = result.generation;
            if (upload.supported) {
                if (upload.hevc && String(result.codec_revision) !== String(upload.codecRevision)) throw new Error("Video codec revision changed");
                if (upload.codecs) upload.codec = selectedCodec(upload, result.codec, result.hevc_level_id);
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
    publication.cancelPreview?.();
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
    return !!publication && publications.get(publication.slot) === publication && !publication.cancelled && mediaScopeIsCurrent(publication.scope) && V().state.pc === publication.pc && publication.track.readyState !== "ended";
}

// toBlob cannot be aborted. Keep at most one native encode outstanding even
// when a timeout or source replacement retires its caller before the callback.
// A browser that never calls back suppresses future previews, not the stream.
let previewEncodingPending = false;
function previewJPEG(publication, canvas) {
    return new Promise((resolve, reject) => {
        let settled = false, reader;
        const finish = (error, jpeg) => {
            if (settled) return;
            settled = true;
            clearTimeout(deadline);
            publication.track.removeEventListener("ended", cancel);
            if (publication.cancelPreview === cancel) publication.cancelPreview = null;
            if (reader?.readyState === 1) reader.abort();
            if (error) reject(error); else resolve(jpeg);
        };
        const cancel = () => finish(new Error("preview cancelled"));
        const deadline = setTimeout(() => finish(new Error("preview encoding timed out")), 5000);
        publication.cancelPreview = cancel;
        publication.track.addEventListener("ended", cancel, { once: true });
        previewEncodingPending = true;
        try {
            canvas.toBlob(blob => {
                previewEncodingPending = false;
                if (settled) return;
                if (!valid(publication)) { cancel(); return; }
                // Bound the input before allocating its base64 representation.
                if (!blob || blob.type !== "image/jpeg" || blob.size === 0 || blob.size > 49152) {
                    finish(new Error("preview JPEG unavailable or too large"));
                    return;
                }
                try {
                    reader = new FileReader();
                    reader.onload = () => {
                        if (settled) return;
                        if (!valid(publication)) { cancel(); return; }
                        const url = reader.result;
                        const jpeg = typeof url === "string" && url.startsWith("data:image/jpeg;base64,") ? url.slice(23) : "";
                        if (!jpeg || jpeg.length > 65536) finish(new Error("invalid preview JPEG"));
                        else finish(null, jpeg);
                    };
                    reader.onerror = () => finish(new Error("preview JPEG read failed"));
                    reader.onabort = cancel;
                    reader.readAsDataURL(blob);
                } catch (error) { finish(error); }
            }, "image/jpeg", 0.55);
        } catch (error) { previewEncodingPending = false; finish(error); }
    });
}

async function capturePreview(publication) {
    if (!valid(publication)) return;
    try {
        if (previewEncodingPending) return;
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
        const jpeg = await previewJPEG(publication, canvas);
        if (!valid(publication) || jpeg.length > 65536) return;
        await streamRequest(publication.scope, { action: "preview_upload", slot: "screen", generation: publication.generation, jpeg });
    } catch (error) {
        if (valid(publication)) V().sysMsg(t("streams.previewFailed", { error: String(error) }));
    } finally {
        if (publication.video) { publication.video.pause(); publication.video.srcObject = null; publication.video = null; }
        if (valid(publication)) publication.timer = setTimeout(() => { void capturePreview(publication); }, 120000);
    }
}
