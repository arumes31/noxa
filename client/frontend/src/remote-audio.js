// remote-audio.js — remote voice and shared-audio playback ownership.
import { SpatialVoice } from "./positional-audio.js";
import { makeLimiter, personalVolumeGain, registerUserChain, unregisterUserChain, createAudioLevelSampler, getUserShareVolume, isUserShareMuted, onShareAudioChange, setDucking, attachUserNormalizer, detachUserNormalizer, detachAllUserNormalizers, resumeAudioPlayback, createRemoteAudioSource } from "./audio.js";
import { parseTrackID } from "./video.js";
import { t } from "./i18n.js";
import { watchAudioOutput } from "./microphone-recovery.js";

export function createRemoteAudio({ state, toast, sysMsg, voiceEpoch }) {
    // Output settings: volume + sink for remote media elements.
    let lastOutputDevice = "";
    let outputWarningShown = false;
    let outputSelection = 0;

    async function selectAudioOutput(target) {
        const deviceID = state.settings?.playback_device_id || "";
        if (deviceID !== lastOutputDevice) {
            lastOutputDevice = deviceID;
            outputWarningShown = false;
            outputSelection++;
        }
        if (typeof target.setSinkId !== "function") return;
        const selection = outputSelection;
        try {
            await target.setSinkId(deviceID);
        } catch {
            if (selection !== outputSelection || deviceID !== (state.settings?.playback_device_id || "") || target.state === "closed" || outputWarningShown) return;
            outputWarningShown = true;
            toast(t("audio.outputSwitchFailed"), "warn");
        }
    }

    function applyOutputSettings(el) {
        const s = state.settings || {};
        el.volume = Math.min(1, (s.volume ?? 100) / 100);
        void selectAudioOutput(el);
        el.muted = state.deafened;
        if (remoteChain.master) {
            remoteChain.master.gain.value = state.deafened ? 0 : Math.min(2, (s.volume ?? 100) / 100);
        }
    }

    // Remote audio WebAudio chain. One shared context carries every publisher:
    // per-track source -> [per-track normalizer] -> per-user gain -> per-user mute
    // -> master gain (volume) -> [limiter] -> destination. Per-publisher tracks
    // (track ID = publisher client ID) make per-user volume/mute/auto-level
    // audible; the registries themselves live in audio.js.
    const remoteChain = { ctx: null, master: null };
    watchAudioOutput(() => remoteChain.ctx && state.settings?.playback_device_id, () => toast(t("audio.outputDisconnected"), "warn"));

    function resumeRemoteAudio() {
        for (const { playback } of [...remoteTracks.values(), ...shareAudio.values()]) {
            if (playback.paused) void playback.play().catch(() => {});
        }
        void resumeAudioPlayback(remoteChain.ctx).catch(() => {
            toast(t("runtime.playbackFailed"), "warn");
        });
    }
    window.addEventListener("pointerdown", resumeRemoteAudio, { passive: true });
    window.addEventListener("keydown", resumeRemoteAudio);
    window.addEventListener("focus", resumeRemoteAudio);
    document.addEventListener("visibilitychange", () => {
        if (document.visibilityState === "visible") resumeRemoteAudio();
    });
    // remoteTracks maps the receiver MediaStreamTrack OBJECT -> {key, trackID,
    // src, gain, mute, uid} for per-track teardown when a publisher leaves or
    // voice is stopped. It must not be keyed by track.id: Chromium derives a
    // receiver track's id from the a=msid it was created with and keeps it when
    // the server reuses that media section for another publisher. After a
    // speaker leaves and rejoins, two live receivers then share one id, and an
    // id-keyed attach for one tore down the other's chain: that listener
    // stopped hearing exactly one speaker.
    const remoteTracks = new Map();
    // trackKeys gives every receiver track object a unique string for the
    // id-keyed helpers (normalizer, spatial voice).
    const trackKeys = new WeakMap();
    let trackKeySequence = 0;
    const trackKey = track => {
        let key = trackKeys.get(track);
        if (!key) { key = `${track.id}#${++trackKeySequence}`; trackKeys.set(track, key); }
        return key;
    };
    // remoteEntry resolves a track object, or (legacy callers) a track id when
    // exactly one attached track carries it.
    const remoteEntry = trackOrID => {
        if (trackOrID && typeof trackOrID === "object") return remoteTracks.get(trackOrID);
        const matches = [...remoteTracks.values()].filter(entry => entry.track.id === trackOrID);
        return matches.length === 1 ? matches[0] : matches.find(entry => entry.track.readyState !== "ended") || null;
    };
    let spatialVoice = null;
    let positionReadPending = false;
    let spatialScope = "";

    function reconcileSpatialVoice() {
        const scope = JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID, voiceEpoch()]);
        if (scope !== spatialScope) {
            spatialScope = scope;
            spatialVoice?.reset();
        }
        spatialVoice?.retainPeers(state.clients.filter(client => client.channel_id === state.myChannelID && client.client_id !== state.myClientID).map(client => client.client_id));
        spatialVoice?.update(!!state.settings?.positional_audio);
    }

    // The native file source is read only while the user has opted in and joined
    // voice. Capture both server and voice scope before any asynchronous work.
    setInterval(async () => {
        const enabled = !!state.settings?.positional_audio;
        reconcileSpatialVoice();
        if (!enabled || positionReadPending || !state.myChannelID || !state.pc || state.pc.connectionState === "closed") return;
        const { activeTabID: tabID, serverGeneration: generation, myChannelID: channelID } = state;
        const epoch = voiceEpoch();
        const current = () => state.settings?.positional_audio && state.activeTabID === tabID && state.serverGeneration === generation && state.myChannelID === channelID && voiceEpoch() === epoch;
        positionReadPending = true;
        try {
            const position = await window.go.main.App.ReadPositionalInput();
            if (!current()) return;
            spatialVoice?.local(position);
            spatialVoice?.update(true);
            await window.go.main.App.PublishPositionForTab(tabID, { channel_id: channelID, context: position.context, x: position.x, y: position.y, z: position.z });
        } catch {
            // A game may not be running. Expiry restores ordinary voice playback.
        } finally { positionReadPending = false; }
    }, 250);

    // ensureRemoteChain builds the shared processing tail once per voice session.
    function ensureRemoteChain() {
        if (remoteChain.ctx) { resumeRemoteAudio(); return true; }
        try {
            const ctx = new (window.AudioContext || window.webkitAudioContext)();
            remoteChain.ctx = ctx;
            spatialVoice = new SpatialVoice(ctx);
            const master = ctx.createGain();
            remoteChain.master = master;
            master.gain.value = state.deafened ? 0 : Math.min(2, (state.settings?.volume ?? 100) / 100);

            // (52) voice limiter/compressor (default on). Gain normalization (53)
            // is per publisher and lives in attachRemoteAudio.
            let out = master;
            if (state.settings?.voice_limiter !== false) {
                const comp = makeLimiter(ctx);
                master.connect(comp);
                out = comp;
            }
            out.connect(ctx.destination);

            // Output device selection where supported (Chrome 110+).
            void selectAudioOutput(ctx);
            resumeRemoteAudio();
            return true;
        } catch (e) {
            sysMsg(t("runtime.remoteAudioFailed", { error: String(e) }));
            return false;
        }
    }

    // attachRemoteAudio adds one publisher's audio track to the shared chain.
    // publisher is the resolved state.clients entry (or null when unknown); its
    // unique ID keys the per-user volume/mute registry from audio.js.
    function attachRemoteAudio(track, publisher, trackID = track.id) {
        detachRemoteTrack(track); // repairing the same receiver replaces its chain
        if (!ensureRemoteChain()) return;
        try {
            const ctx = remoteChain.ctx;
            // a MediaStreamAudioSourceNode taps only the first audio track of the
            // stream it is built from, so the per-user volume (1) and local mute
            // (2) chains would all follow one publisher if several tracks share a
            // stream: wrap this track alone.
            const { src, playback } = createRemoteAudioSource(ctx, track);
            const gain = ctx.createGain();
            const mute = ctx.createGain();
            // (53) auto-level per publisher: keyed by track ID, inserted behind
            // this publisher's source and returning src unchanged when the setting
            // is off. A master-bus normalizer could not lift a quiet speaker out
            // of a loud one.
            const key = trackKey(track);
            const head = attachUserNormalizer(ctx, key, src);
            head.connect(gain);
            gain.connect(mute);
            spatialVoice.attach(key, mute, remoteChain.master, publisher?.client_id || "");

            const uid = publisher?.unique_id || "";
            const entry = { track, key, trackID, src, playback, gain, mute, uid };
            remoteTracks.set(track, entry);
            if (uid) registerUserChain(uid, gain, mute);

            entry.ended = () => {
                if (remoteTracks.get(track) === entry) detachRemoteTrack(track);
            };
            track.addEventListener("ended", entry.ended);
        } catch (e) {
            sysMsg(t("runtime.remoteAudioFailed", { error: String(e) }));
        }
    }

    function readRemoteAudioLevel(trackOrID) {
        const entry = remoteEntry(trackOrID);
        if (!entry || !remoteChain.ctx || remoteChain.ctx.state === "closed") return null;
        entry.sampleLevel ||= createAudioLevelSampler(remoteChain.ctx, entry.src);
        return entry.sampleLevel();
    }

    // hasRemoteAudio reports whether this receiver track plays, optionally as
    // the given publisher track ID (MSID).
    function hasRemoteAudio(track, trackID) {
        const entry = remoteTracks.get(track);
        return !!entry && (trackID === undefined || entry.trackID === trackID) &&
            !!entry.playback.srcObject?.getAudioTracks().includes(track) && remoteChain.ctx?.state !== "closed";
    }

    // resolveTrackUsers re-attributes tracks whose renegotiation arrived before
    // the publisher's user_joined event: at ontrack time the client list has no
    // entry yet, and without a second pass the per-user volume (1) and local mute
    // (2) chains are never registered for that publisher for the whole session.
    function resolveTrackUsers() {
        for (const [trackID, u] of state.trackUsers) {
            if (u.unique_id) continue;
            const { clientID } = parseTrackID(u.track_id || trackID);
            const publisher = state.clients.find((c) => String(c.client_id) === clientID);
            if (!publisher) continue;
            state.trackUsers.set(trackID, {
                client_id: publisher.client_id, unique_id: publisher.unique_id, nickname: publisher.nickname, track_id: u.track_id,
            });
            // (14) the share chain needs the unique ID too, or its publisher can
            // never be exempted from ducking.
            const sa = shareAudio.get(clientID);
            if (sa && !sa.uid && publisher.unique_id) {
                sa.uid = publisher.unique_id;
                applyShareAudio(clientID);
            }
        }
        // Microphone chains are resolved from their own publisher track ID, not
        // through state.trackUsers (keyed by a browser track.id two receivers
        // may share).
        for (const entry of remoteTracks.values()) {
            if (entry.uid) continue;
            const { clientID, slot } = parseTrackID(entry.trackID);
            const publisher = !slot && state.clients.find((c) => String(c.client_id) === clientID);
            if (!publisher?.unique_id) continue;
            spatialVoice?.peer(entry.key, publisher.client_id);
            entry.uid = publisher.unique_id;
            registerUserChain(publisher.unique_id, entry.gain, entry.mute);
        }
    }

    // ---------------------------------------------------------------------------
    // Shared system audio (70) — a screen share's audio arrives on its own slot
    // and gets its own chain, NOT the publisher's voice chain.
    //
    // Chosen behaviour: the per-user volume slider and local mute (1/2) are
    // controls for a PERSON's voice and only touch the microphone. A sharer's
    // system audio has its own mute + volume on the screen tile's context menu,
    // because the reason to mute someone is usually that they are noisy while you
    // are watching what they share — killing the show with the talker is wrong.
    // Priority-speaker ducking (14) does apply: it exists to make one voice
    // audible over everything else, program audio included. Preferences persist by
    // unique ID independently from settings.user_volumes/muted_users.
    // ---------------------------------------------------------------------------

    // shareAudio maps publisher client ID -> {trackID, src, gain, uid, volume, muted}.
    const shareAudio = new Map();
    onShareAudioChange(uid => {
        for (const [clientID, entry] of shareAudio) if (!uid || entry.uid === uid) applyShareAudio(clientID);
    });

    // SHARE_DUCK_FACTOR must match audio.js's DUCK_FACTOR: these chains are not in
    // its per-user registry, so setDucking cannot reach them.
    const SHARE_DUCK_FACTOR = 0.25;
    let shareDuckActive = false;
    let shareDuckExempt = new Set();
    // Multiview focus (design 45): publisher client ID -> factor for its
    // screen-share audio only. Voice chains are never in this map.
    let shareStageGains = new Map();

    function setShareAudioStageGains(gains) {
        shareStageGains = new Map([...(gains || [])].map(([clientID, factor]) => [String(clientID),
            Number.isFinite(factor) ? Math.min(1, Math.max(0, factor)) : 1]));
        for (const clid of shareAudio.keys()) applyShareAudio(clid);
    }

    // applyDucking is setDucking plus the share chains it cannot see.
    function applyDucking(active, exceptUIDs) {
        shareDuckActive = active;
        shareDuckExempt = new Set(exceptUIDs || []);
        setDucking(active, exceptUIDs);
        for (const clid of shareAudio.keys()) applyShareAudio(clid);
    }

    function applyShareAudio(clientID) {
        const n = shareAudio.get(String(clientID));
        if (!n) return;
        if (n.uid) {
            n.volume = getUserShareVolume(n.uid) * 100;
            n.muted = isUserShareMuted(n.uid);
        }
        const duck = shareDuckActive && !shareDuckExempt.has(n.uid) ? SHARE_DUCK_FACTOR : 1;
        const stage = shareStageGains.get(String(clientID)) ?? 1;
        n.gain.gain.value = (n.muted ? 0 : personalVolumeGain(n.volume / 100)) * duck * stage;
    }

    function attachShareAudio(track, clientID, publisher) {
        const clid = String(clientID);
        detachShareAudio(clid); // re-attach after an ICE restart replaces the track
        if (!ensureRemoteChain()) return;
        try {
            const ctx = remoteChain.ctx;
            const { src, playback } = createRemoteAudioSource(ctx, track);
            const gain = ctx.createGain();
            src.connect(gain);
            // no auto-level (53) on program audio: it would pump on music and
            // game sound, which is not a quiet speaker that needs lifting.
            gain.connect(remoteChain.master);
            const entry = {
                track, trackID: track.id, src, playback, gain, uid: publisher?.unique_id || "",
                volume: 100, muted: false,
            };
            shareAudio.set(clid, entry);
            applyShareAudio(clid);
            entry.ended = () => {
                if (shareAudio.get(clid) === entry) detachShareAudio(clid);
            };
            track.addEventListener("ended", entry.ended);
        } catch (e) {
            sysMsg(t("runtime.shareAudioFailed", { error: String(e) }));
        }
    }

    function detachShareAudio(clientID) {
        const n = shareAudio.get(String(clientID));
        if (!n) return;
        shareAudio.delete(String(clientID));
        n.track.removeEventListener("ended", n.ended);
        n.playback.pause();
        n.playback.srcObject = null;
        try {
            n.gain.disconnect();
            n.src.disconnect();
        } catch { /* already disconnected */ }
    }

    // detachRemoteTrack removes one publisher's nodes from the shared chain.
    function detachRemoteTrack(trackOrID) {
        const t = remoteEntry(trackOrID);
        if (!t) return;
        remoteTracks.delete(t.track);
        t.track.removeEventListener("ended", t.ended);
        spatialVoice?.detach(t.key);
        t.playback.pause();
        t.playback.srcObject = null;
        if (t.uid) unregisterUserChain(t.uid, t.gain);
        detachUserNormalizer(t.key); // (53) no-op when normalization is off
        try {
            t.mute.disconnect();
            t.gain.disconnect();
            t.src.disconnect();
        } catch { /* already disconnected */ }
    }

    function detachRemoteAudio() {
        for (const track of [...remoteTracks.keys()]) detachRemoteTrack(track);
        spatialVoice = null;
        for (const clid of [...shareAudio.keys()]) detachShareAudio(clid); // (70)
        detachAllUserNormalizers(); // (53) stops the shared auto-level ticker
        if (remoteChain.ctx) {
            remoteChain.ctx.close().catch(() => {});
            remoteChain.ctx = null;
            remoteChain.master = null;
        }
        state.trackUsers.clear();
    }

    return { selectAudioOutput, applyOutputSettings, remoteChain, get spatialVoice() { return spatialVoice; }, reconcileSpatialVoice, attachRemoteAudio, hasRemoteAudio, readRemoteAudioLevel, resolveTrackUsers, shareAudio, applyDucking, applyShareAudio, setShareAudioStageGains, attachShareAudio, detachRemoteTrack, detachShareAudio, detachRemoteAudio };
}
