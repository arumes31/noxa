import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { getUserShareVolume, isUserShareMuted, onShareAudioChange, setUserShareMuted, setUserShareVolume } from "./audio.js";
import { applyCameraPreview } from "./camera-capture.js";
import { displayAudioOptions, validateDisplayAudio } from "./display-audio.js";

const sources = ["camera", "screen"];
const liveTrack = (capture, kind) => capture?.stream.getTracks().find(track => track.kind === kind && track.readyState === "live") || null;

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
    const canPublish = () => current(owner) && owner.call.participants.some(peer => peer.unique_id === owner.uid && peer.state === "accepted");
    const state = () => ({ camera: !!liveTrack(captures.camera, "video"), screen: !!liveTrack(captures.screen, "video") });

    function sendState(peer) {
        if (peer.mediaChannel?.readyState === "open") peer.mediaChannel.send(JSON.stringify(state()));
    }
    function syncPeer(peer, uid) {
        const task = (peer.mediaChain || Promise.resolve()).then(async () => {
            if (!acceptedPeer(owner, uid) || owner.peers.get(uid) !== peer) return;
            const tracks = { camera: liveTrack(captures.camera, "video"), screen: liveTrack(captures.screen, "video"), screenAudio: liveTrack(captures.screen, "audio") };
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
                const track = peer.mediaTracks?.[source], existing = peer.mediaTiles?.[source];
                const visible = acceptedPeer(owner, uid) && peer.remoteMedia?.[source] === true && track?.readyState === "live";
                if (!visible && existing) { releaseTile(existing); delete peer.mediaTiles[source]; }
                if (visible && !existing) {
                    peer.mediaTiles ||= {};
                    peer.mediaTiles[source] = tile(new MediaStream([track]), uid, source, false);
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
    async function toggle(source) {
        if (!canPublish()) return;
        if (captures[source] || requests[source]) { stop(source); return; }
        const request = { controller: new AbortController() }; requests[source] = request; render(owner);
        const requestedAudioMode = audioMode;
        let captureAudioMode = requestedAudioMode;
        let capture;
        try {
            if (source === "camera") {
                const { captureCamera } = await import("./camera-capture.js");
                if (!canPublish() || requests[source] !== request) return;
                capture = await captureCamera(window.__noxa.state.settings || {}, undefined, { signal: request.controller.signal });
            } else {
                const video = { frameRate: { ideal: 15, max: 30 } };
                if (captureAudioMode === "application") video.displaySurface = "window";
                let stream;
                try {
                    stream = await navigator.mediaDevices.getDisplayMedia({ video, ...displayAudioOptions(captureAudioMode) });
                } catch (error) {
                    if (!canPublish() || requests[source] !== request) return;
                    if (captureAudioMode === "none" || error.name === "NotAllowedError" || error.name === "AbortError") throw error;
                    stream = await navigator.mediaDevices.getDisplayMedia({ video, ...displayAudioOptions("none") });
                    captureAudioMode = "none";
                }
                capture = { stream, stop: () => stream.getTracks().forEach(track => track.stop()) };
                if (!canPublish() || requests[source] !== request) { capture.stop(); return; }
                audioMode = validateDisplayAudio(stream, captureAudioMode);
            }
            if (!canPublish() || requests[source] !== request) { capture.stop(); return; }
            const track = liveTrack(capture, "video");
            if (!track) throw new Error(t("call.media.noVideo"));
            captures[source] = capture; requests[source] = null;
            track.addEventListener("ended", () => { if (captures[source] === capture) stop(source); }, { once: true });
            await syncPeers();
            if (canPublish() && captures[source] === capture) {
                updateGrid(); render(owner);
                if (source === "screen" && requestedAudioMode !== "none" && audioMode === "none") {
                    window.__noxa.toast(t(requestedAudioMode === "application" ? "share.applicationUnavailable" : "share.audioNotSelected"), "warn");
                }
            }
        } catch (error) {
            capture?.stop();
            if (requests[source] === request || captures[source] === capture) {
                requests[source] = null; captures[source] = null;
                if (current(owner)) { updateGrid(); render(owner); void syncPeers().catch(report); report(error); }
            }
        }
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
                peer.remoteMedia = { camera: value.camera, screen: value.screen }; updateGrid();
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
        for (const source of sources) { requests[source]?.controller.abort(); requests[source] = null; captures[source]?.stop(); captures[source] = null; }
        for (const element of localTiles.values()) releaseTile(element);
        localTiles.clear(); grid.replaceChildren();
    }
    return { grid, controls, syncPeer, attachChannel, receiveVideo, closePeer, close, updateGrid };
}
