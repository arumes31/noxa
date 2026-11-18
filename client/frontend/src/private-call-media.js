import { retainDisplayCapture, stopDisplayCapture } from "./display-capture-lease.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { getUserShareVolume, isUserShareMuted, onShareAudioChange, setUserShareMuted, setUserShareVolume } from "./audio.js";
import { applyCameraPreview } from "./camera-capture.js";
import { CALL_E2EE_MARKER, announcesCallE2EE } from "./call-media-e2ee.js";
import { nativeStandInStream, nativeFailureKey } from "./native-share.js";

const sources = ["camera", "screen"];
const liveTrack = (capture, kind) => capture?.stream.getTracks().find(track => track.kind === kind && track.readyState === "live") || null;
// A native share's stream is a local stand-in: its media goes over the
// screen sub-connections (Go), never the main connection's screen slots.
const sentTrack = (capture, kind) => capture?.native ? null : liveTrack(capture, kind);
const app = () => window.go?.main?.App;

/**
 * Why a screen share into the call is refused, as an i18n key, or "": every
 * accepted participant must view native shares, which arrive on a screen
 * sub-connection only apps advertising "screen-sub" open.
 * peers: the accepted participants' peer objects.
 */
export function callShareRefusal(peers) {
    if (peers.some(peer => peer.screenSubSupported === false)) return "call.media.viewerTooOld";
    return peers.length && peers.every(peer => peer.screenSubSupported === true) ? "" : "call.media.shareNotReady";
}

// Reserve separate slots in the first offer. Later camera/screen toggles use
// replaceTrack, avoiding concurrent renegotiation across a group-call mesh.
export function offerCallMedia(peer) {
    peer.mediaSenders = {
        camera: peer.pc.addTransceiver("video", { direction: "sendrecv" }).sender,
        screen: peer.pc.addTransceiver("video", { direction: "sendrecv" }).sender,
        screenAudio: peer.pc.addTransceiver("audio", { direction: "sendrecv" }).sender,
    };
}

export function answerCallMedia(peer) {
    const video = peer.pc.getTransceivers().filter(item => item.receiver.track.kind === "video");
    const audio = peer.pc.getTransceivers().filter(item => item.receiver.track.kind === "audio");
    for (const transceiver of [...video.slice(0, 2), ...audio.slice(1, 2)]) transceiver.direction = "sendrecv";
    peer.mediaSenders = { camera: video[0]?.sender, screen: video[1]?.sender, screenAudio: audio[1]?.sender };
}

export function createCallMedia(owner, { current, acceptedPeer, label, report, render }) {
    const captures = { camera: null, screen: null };
    const requests = { camera: null, screen: null };
    const localTiles = new Map();
    const grid = document.createElement("div"); grid.className = "call-media-grid";
    window.addEventListener("noxa-camera-preferences-changed", updateGrid);
    let audioMode = "none";
    let picking = false; // noXa's share picker is open
    let pendingNative = null; // the native share whose Start is in flight
    let nativeTargets = "";
    const offNative = window.runtime?.EventsOn?.("native_share", nativeEvent);
    const canPublish = () => current(owner) && owner.call.participants.some(peer => peer.unique_id === owner.uid && peer.state === "accepted");
    // e2ee announces call encryption (#12); older apps omit it.
    const state = () => ({ camera: !!liveTrack(captures.camera, "video"), screen: !!liveTrack(captures.screen, "video"), e2ee: CALL_E2EE_MARKER });

    function sendState(peer) {
        if (peer.mediaChannel?.readyState === "open") peer.mediaChannel.send(JSON.stringify(state()));
    }
    function syncPeer(peer, uid) {
        const task = (peer.mediaChain || Promise.resolve()).then(async () => {
            if (!acceptedPeer(owner, uid) || owner.peers.get(uid) !== peer) return;
            const tracks = { camera: sentTrack(captures.camera, "video"), screen: sentTrack(captures.screen, "video"), screenAudio: sentTrack(captures.screen, "audio") };
            for (const [slot, sender] of Object.entries(peer.mediaSenders || {})) {
                if (sender && sender.track !== tracks[slot]) await sender.replaceTrack(tracks[slot]);
            }
            if (acceptedPeer(owner, uid) && owner.peers.get(uid) === peer) sendState(peer);
        });
        peer.mediaChain = task.catch(() => {});
        return task;
    }
    const syncPeers = () => Promise.all([...owner.peers].map(([uid, peer]) => syncPeer(peer, uid)));

    function tile(stream, uid, source, local) {
        const figure = document.createElement("figure"); figure.className = "call-media-tile";
        figure.dataset.source = source; figure.dataset.local = String(local);
        const video = document.createElement("video");
        video.autoplay = true; video.playsInline = true; video.muted = true; video.srcObject = stream;
        if (local && source === "camera") applyCameraPreview(video, window.__noxa.state.settings);
        const caption = document.createElement("figcaption");
        caption.textContent = t(`call.media.${source}Tile`, { name: local ? t("call.media.you") : label(uid) });
        video.setAttribute("aria-label", caption.textContent);
        const expand = document.createElement("button"); expand.type = "button"; expand.className = "call-media-expand";
        expand.innerHTML = icon("screen"); expand.setAttribute("aria-label", t("call.media.expand", { name: caption.textContent }));
        expand.onclick = () => { if (figure.requestFullscreen) void figure.requestFullscreen().catch(report); };
        figure.append(video, caption, expand);
        if (source === "screen" && !local) {
            const controls = document.createElement("div"); controls.className = "call-share-audio";
            const mute = document.createElement("button"); mute.type = "button";
            const slider = document.createElement("input"); slider.type = "range"; slider.min = "0"; slider.max = "200"; slider.step = "5";
            const output = document.createElement("output");
            const refresh = () => {
                const percent = Math.round(getUserShareVolume(uid) * 100), muted = isUserShareMuted(uid);
                mute.textContent = t(muted ? "polish.sharedAudioUnmute" : "polish.sharedAudioMute");
                mute.setAttribute("aria-pressed", String(muted));
                slider.setAttribute("aria-label", t("polish.sharedAudio"));
                slider.setAttribute("aria-valuetext", `${percent}%`);
                if (slider.ownerDocument.activeElement !== slider) slider.value = String(percent);
                output.textContent = `${percent}%`;
            };
            mute.onclick = () => { void setUserShareMuted(uid, !isUserShareMuted(uid)).catch(report); };
            slider.oninput = () => { void setUserShareVolume(uid, Number(slider.value)).catch(report); };
            controls.append(mute, slider, output); figure.append(controls);
            const unsubscribe = onShareAudioChange(changed => { if (!changed || changed === uid) refresh(); });
            figure.releaseAudioControls = unsubscribe;
            refresh();
        }
        return figure;
    }
    function releaseTile(element) {
        element.releaseAudioControls?.();
        const video = element.querySelector("video"); video.pause(); video.srcObject = null; element.remove();
    }
    function updateGrid() {
        for (const source of sources) {
            const capture = captures[source], existing = localTiles.get(source);
            if (existing && existing.querySelector("video").srcObject !== capture?.stream) { releaseTile(existing); localTiles.delete(source); }
            if (capture && !localTiles.has(source)) localTiles.set(source, tile(capture.stream, owner.uid, source, true));
        }
        const tiles = [...localTiles.values()];
        for (const [uid, peer] of owner.peers) {
            for (const source of sources) {
                // A native share arrives on the peer's screen sub-connection.
                const track = source === "screen" && peer.screenTrack?.readyState === "live" ? peer.screenTrack : peer.mediaTracks?.[source], existing = peer.mediaTiles?.[source];
                const visible = acceptedPeer(owner, uid) && peer.remoteMedia?.[source] === true && track?.readyState === "live";
                if (existing && (!visible || existing.mediaTrack !== track)) { releaseTile(existing); delete peer.mediaTiles[source]; }
                if (visible && !peer.mediaTiles?.[source]) {
                    peer.mediaTiles ||= {};
                    peer.mediaTiles[source] = tile(new MediaStream([track]), uid, source, false);
                    peer.mediaTiles[source].mediaTrack = track;
                }
                if (peer.mediaTiles?.[source]) tiles.push(peer.mediaTiles[source]);
            }
        }
        if (grid.children.length !== tiles.length || tiles.some((element, index) => grid.children[index] !== element)) grid.replaceChildren(...tiles);
        for (const video of grid.querySelectorAll('[data-local="true"][data-source="camera"] video')) applyCameraPreview(video, window.__noxa.state.settings);
        grid.hidden = tiles.length === 0;
        owner.panel.classList.toggle("has-media", tiles.length > 0);
        for (const video of grid.querySelectorAll("video")) void video.play().catch(() => {});
    }
    function stop(source) {
        requests[source]?.controller.abort();
        requests[source] = null;
        const capture = captures[source]; captures[source] = null;
        capture?.stop();
        updateGrid();
        if (current(owner)) { render(owner); void syncPeers().catch(report); }
    }
    // isActive reports a live or pending capture for source.
    const isActive = source => !!captures[source] || !!requests[source] || (source === "screen" && picking);
    const acceptedPeers = () => [...owner.peers].filter(([uid]) => acceptedPeer(owner, uid));
    const refusal = () => callShareRefusal(acceptedPeers().map(([, peer]) => peer));
    // Follows membership: a joiner gets a sub-connection once its support is
    // known; a joiner whose app cannot view native shares ends the share.
    function syncNativeTargets() {
        const native = captures.screen?.native;
        if (!native || !canPublish()) return;
        const peers = acceptedPeers();
        if (peers.some(([, peer]) => peer.screenSubSupported === false)) { stopNative("call.media.viewerTooOld"); return; }
        const targets = peers.filter(([, peer]) => peer.screenSubSupported).map(([uid]) => uid).sort();
        if (targets.join(" ") === nativeTargets) return;
        nativeTargets = targets.join(" ");
        void Promise.resolve().then(() => app().UpdateNativeCallShareTargetsForTab(native.tabID, native.id, targets)).catch(() => {});
    }
    // stopNative ends the native share with a notice (an i18n key).
    function stopNative(notice) {
        if (!captures.screen?.native) return;
        stop("screen");
        window.__noxa.toast(t(notice), "error");
    }
    // Go's lifecycle events of this call's native share.
    function nativeEvent(payload) {
        let event = payload;
        if (typeof payload === "string") { try { event = JSON.parse(payload); } catch { return; } }
        const ours = capture => !!capture?.native && event?.share === capture.native.id && event.tab === capture.native.tabID;
        if (ours(pendingNative)) {
            if (event.state === "ended" || event.state === "failed") { pendingNative.ended = true; pendingNative.failure = event.state === "failed" ? event.reason || "unknown" : ""; }
            else if (event.state === "audio") pendingNative.audio = event.reason || "none";
            return;
        }
        if (!ours(captures.screen) || !current(owner)) return;
        if (event.state === "minimized") window.__noxa.toast(t("share.nativeMinimized"), "warn");
        else if (event.state === "audio") nativeAudioNotice(event.reason);
        else if (event.state === "ended") stop("screen");
        else if (event.state === "failed") stopNative(nativeFailureKey(event.reason));
    }
    function nativeAudioNotice(reason) {
        window.__noxa.toast(t(reason === "system" ? "share.nativeAudioSystem" : "share.nativeAudioUnavailable"), "warn");
    }
    // Starts a native share into the call (noXa's picker chose the source).
    async function startNative({ source, max_width, max_height, fps, bitrate, audioMode: chosen }) {
        if (!canPublish() || captures.screen || requests.screen) return false;
        const targets = acceptedPeers().filter(([, peer]) => peer.screenSubSupported).map(([uid]) => uid).sort();
        if (!targets.length) return false;
        audioMode = chosen;
        const request = { controller: new AbortController() }; requests.screen = request; render(owner);
        const id = crypto.randomUUID(), tabID = owner.tabID;
        const stream = nativeStandInStream(t("share.nativeActive"));
        // The capture lease keeps the display awake and raises this process's priority.
        retainDisplayCapture(stream);
        const capture = { stream, native: { id, tabID }, stop: () => {
            stopDisplayCapture(stream);
            void Promise.resolve().then(() => app().StopNativeScreenShareForTab(tabID, id)).catch(() => {});
        } };
        pendingNative = capture;
        try {
            await app().StartNativeCallShareForTab(tabID, { id, call_id: owner.call.id, targets, source, max_width, max_height, fps, bitrate, audio: chosen });
        } catch {
            capture.ended = true;
        } finally { if (pendingNative === capture) pendingNative = null; }
        if (capture.ended || requests.screen !== request || !canPublish()) {
            capture.stop();
            if (requests.screen === request) { requests.screen = null; if (current(owner)) render(owner); }
            if (capture.failure && current(owner)) window.__noxa.toast(t(nativeFailureKey(capture.failure)), "error");
            return false;
        }
        captures.screen = capture; requests.screen = null; nativeTargets = targets.join(" ");
        if (capture.audio) nativeAudioNotice(capture.audio);
        await syncPeers().catch(report);
        if (captures.screen === capture) { updateGrid(); render(owner); }
        return true;
    }
    // noXa's share picker (one confirmation); refused while a participant
    // cannot view native shares.
    async function pickNative() {
        const reason = refusal();
        if (reason) { window.__noxa.toast(t(reason), "warn"); return; }
        picking = true; render(owner);
        try {
            const { openCallShareDialog } = await import("./video.js");
            if (!canPublish()) { picking = false; return; }
            openCallShareDialog({ audio: audioMode, startNative,
                onClose: () => { picking = false; if (current(owner)) render(owner); } });
        } catch (error) { picking = false; if (current(owner)) { render(owner); report(error); } }
    }
    async function toggle(source) {
        if (!canPublish()) return;
        if (isActive(source)) { stop(source); return; }
        await start(source);
    }
    // start only ever turns source on: an active or pending capture is kept,
    // so a repeated "start video" intent cannot switch the camera off. The
    // screen opens noXa's picker, which starts the share natively.
    async function start(source) {
        if (!canPublish() || isActive(source)) return false;
        if (source === "screen") { await pickNative(); return false; }
        const request = { controller: new AbortController() }; requests[source] = request; render(owner);
        let capture;
        try {
            const { captureCamera } = await import("./camera-capture.js");
            if (!canPublish() || requests[source] !== request) return false;
            capture = await captureCamera(window.__noxa.state.settings || {}, undefined, { signal: request.controller.signal });
            if (!canPublish() || requests[source] !== request) { capture.stop(); return false; }
            const track = liveTrack(capture, "video");
            if (!track) throw new Error(t("call.media.noVideo"));
            captures[source] = capture; requests[source] = null;
            track.addEventListener("ended", () => { if (captures[source] === capture) stop(source); }, { once: true });
            await syncPeers();
            if (canPublish() && captures[source] === capture) { updateGrid(); render(owner); }
        } catch (error) {
            capture?.stop();
            if (requests[source] === request || captures[source] === capture) {
                requests[source] = null; captures[source] = null;
                if (current(owner)) { updateGrid(); render(owner); void syncPeers().catch(report); report(error); }
            }
        }
        return !!capture && captures[source] === capture;
    }
    function controls() {
        const fragment = document.createDocumentFragment();
        for (const source of sources) {
            const active = !!captures[source], pending = !!requests[source];
            const button = document.createElement("button"); button.type = "button"; button.className = "voice-control call-control";
            button.innerHTML = icon(source === "camera" ? active ? "cameraOff" : "camera" : "screen");
            const text = document.createElement("span"); text.textContent = t(`call.media.${pending ? "cancel" : active ? "stop" : "start"}.${source}`);
            button.append(text); button.setAttribute("aria-pressed", String(active));
            button.onclick = () => { void toggle(source); }; fragment.append(button);
        }
        const label = document.createElement("label"); label.className = "call-screen-audio";
        const select = document.createElement("select");
        for (const [value, key] of [["none", "audioNone"], ["application", "audioApplication"], ["system", "audioSystem"]]) {
            const option = document.createElement("option"); option.value = value; option.textContent = t(`share.${key}`); select.append(option);
        }
        select.value = audioMode;
        select.disabled = !!captures.screen || !!requests.screen;
        select.onchange = () => { audioMode = select.value; };
        select.title = t("share.applicationHelp");
        label.append(document.createTextNode(t("share.audio")), select); fragment.append(label);
        return fragment;
    }
    function attachChannel(peer, uid, channel) {
        if (channel.label !== "call-media" || peer.mediaChannel || !acceptedPeer(owner, uid)) { channel.close(); return; }
        peer.mediaChannel = channel;
        channel.onopen = () => { if (acceptedPeer(owner, uid)) sendState(peer); };
        channel.onmessage = event => {
            if (!acceptedPeer(owner, uid) || owner.peers.get(uid) !== peer || typeof event.data !== "string" || event.data.length > 128) return;
            try {
                const value = JSON.parse(event.data);
                if (!value || typeof value.camera !== "boolean" || typeof value.screen !== "boolean") return;
                if (!announcesCallE2EE(value)) { owner.onPeerOutdated?.(uid); return; }
                peer.remoteMedia = { camera: value.camera, screen: value.screen };
                if (!value.screen) peer.screenSub?.close(); // the peer's native share ended
                updateGrid();
            } catch { /* Ignore malformed peer UI state; it cannot authorize media. */ }
        };
        channel.onclose = () => { peer.remoteMedia = {}; if (current(owner)) updateGrid(); };
    }
    function receiveVideo(peer, event) {
        const index = peer.pc.getTransceivers().filter(item => item.receiver.track.kind === "video").indexOf(event.transceiver);
        if (index < 0 || index > 1) return;
        peer.mediaTracks ||= {}; peer.mediaTracks[sources[index]] = event.track;
        event.track.addEventListener("ended", updateGrid, { once: true }); updateGrid();
    }
    function closePeer(peer) {
        if (peer.mediaChannel) { peer.mediaChannel.onopen = null; peer.mediaChannel.onmessage = null; peer.mediaChannel.onclose = null; peer.mediaChannel.close(); }
        for (const element of Object.values(peer.mediaTiles || {})) releaseTile(element);
        peer.mediaTiles = {}; peer.mediaTracks = {}; peer.remoteMedia = {};
    }
    function close() {
        window.removeEventListener("noxa-camera-preferences-changed", updateGrid);
        if (typeof offNative === "function") offNative();
        if (pendingNative) pendingNative.ended = true;
        for (const source of sources) { requests[source]?.controller.abort(); requests[source] = null; captures[source]?.stop(); captures[source] = null; }
        for (const element of localTiles.values()) releaseTile(element);
        localTiles.clear(); grid.replaceChildren();
    }
    return { grid, controls, toggle, start, isActive, syncPeer, attachChannel, receiveVideo, closePeer, close, updateGrid, syncNativeTargets };
}
