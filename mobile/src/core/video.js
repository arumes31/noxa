// video.js — video helpers shared by channel voice and private calls, no DOM
// beyond media elements the camera pipeline needs:
//   codecs     what this device decodes, how it orders receive codecs and
//              which names the SFU accepts in a watch (vp8/h264/h265 only)
//   quality    per-stream receive layer (Msg.VideoQuality, scoped by
//              publisher/slot/generation/session like the desktop's
//              SetStreamVideoQualityForTab)
//   stage      which watched streams are shown and which one has focus
//   camera     front/back camera with an optional privacy blur, sized to the
//              server's media limits
//   waiting    what a private call update means while already in a call

// --- Codecs -------------------------------------------------------------------

/** "video/VP9" -> "vp9"; "video/H264" -> "h264". */
export function codecFamily(codec) {
    return String(codec?.mimeType || "").replace(/^video\//i, "").toLowerCase();
}

const AUXILIARY = new Set(["rtx", "red", "ulpfec", "flexfec-03"]);

/** Whether this looks like an iPhone/iPad WebKit (hardware H.264 is cheapest there). */
export function isIOS(agent = globalThis.navigator?.userAgent || "", touchPoints = globalThis.navigator?.maxTouchPoints || 0) {
    return /iPhone|iPad|iPod/.test(agent) || (/Macintosh/.test(agent) && touchPoints > 1);
}

/** Preferred receive order: H.264 > VP9 > VP8 on iOS, AV1 > VP9 > H.264 > VP8 elsewhere. */
export function codecOrder(ios = isIOS()) {
    return ios ? ["h264", "vp9", "vp8", "av1", "h265"] : ["av1", "vp9", "h264", "vp8", "h265"];
}

/**
 * Orders receiver codec capabilities for setCodecPreferences. Every codec the
 * device decodes stays in the list (the SFU picks among what it offered);
 * unknown families keep their place after the known ones, and RTX/RED/FEC go
 * last so they still pair with the primary payloads.
 */
export function receiveCodecPreferences(codecs = [], { ios = isIOS() } = {}) {
    const order = codecOrder(ios);
    const rank = codec => {
        const family = codecFamily(codec);
        if (AUXILIARY.has(family)) return order.length + 1;
        const index = order.indexOf(family);
        return index < 0 ? order.length : index;
    };
    return codecs.map((codec, index) => ({ codec, index }))
        .sort((a, b) => rank(a.codec) - rank(b.codec) || a.index - b.index)
        .map(entry => entry.codec);
}

/** Video codec families (no RTX/RED/FEC) this device can decode or encode. */
export function codecFamilies(kind = "RTCRtpReceiver") {
    let codecs = [];
    try { codecs = globalThis[kind]?.getCapabilities?.("video")?.codecs || []; } catch { codecs = []; }
    return [...new Set(codecs.map(codecFamily).filter(family => family && !AUXILIARY.has(family)))];
}

/** The relay's H.264 is constrained baseline with packetization-mode 1. */
export function relayH264(codec) {
    const fmtp = String(codec?.sdpFmtpLine || "");
    return codecFamily(codec) === "h264" && /profile-level-id=4200/i.test(fmtp) && /packetization-mode=1/.test(fmtp);
}

/**
 * Codec names for a watch request. The SFU (internal/webrtc
 * videoCodecCapabilities) accepts only vp8, h264 and h265 and rejects the
 * whole watch on anything else, so VP9/AV1 never go on the wire even when
 * the device decodes them; VP8 is the mandatory floor.
 */
export function watchCodecNames(codecs = []) {
    return codecs.some(relayH264) ? ["h264", "vp8"] : ["vp8"];
}

/** One line for diagnostics: decodable families in preference order. */
export function codecCapabilityLine(families = codecFamilies(), ios = isIOS()) {
    const order = codecOrder(ios);
    const known = order.filter(family => families.includes(family));
    const other = families.filter(family => !order.includes(family));
    return [...known, ...other].map(family => family.toUpperCase()).join(" > ") || "—";
}

/** Applies the receive order to the video transceivers the SFU just offered. */
export function preferReceiveCodecs(pc) {
    let codecs = [];
    try { codecs = globalThis.RTCRtpReceiver?.getCapabilities?.("video")?.codecs || []; } catch { return; }
    if (!codecs.length) return;
    const ordered = receiveCodecPreferences(codecs);
    for (const transceiver of pc.getTransceivers()) {
        if (transceiver.receiver.track?.kind !== "video" || transceiver.direction !== "recvonly" || !transceiver.setCodecPreferences) continue;
        try { transceiver.setCodecPreferences(ordered); } catch { /* the browser keeps its own order */ }
    }
}

// --- Receive quality ------------------------------------------------------------

export const QUALITIES = ["auto", "high", "medium", "low"];

/**
 * The layer to request for one watched stream. "auto" leaves the server's
 * default when a single stream is watched; with several, the focused stream
 * and screens get high and the rest mid (receive-quality.js "desired").
 * Low bandwidth always asks for the low layer.
 */
export function wireQuality(preference, { lowBandwidth = false, focused = false, slot = "cam", watched = 1 } = {}) {
    if (lowBandwidth) return "low";
    switch (preference) {
        case "high": return "high";
        case "medium": return "mid";
        case "low": return "low";
        default:
            if (watched <= 1) return "default";
            return focused || slot === "screen" ? "high" : "mid";
    }
}

// --- Stage ------------------------------------------------------------------------

export const DUCKED_VOLUME = 0.25;

/**
 * Watched streams on the stage. Layout "grid" shows them evenly; "focus"
 * shows one large and the others as a strip. Order is user-controlled
 * (swap), focus follows the user or falls back to the first stream.
 */
export class StageState {
    constructor({ layout = "grid" } = {}) {
        this.keys = [];
        this.layout = layout;
        this.focused = null;
    }

    /** Keeps the stage in step with the streams actually watched. */
    sync(keys) {
        const wanted = new Set(keys);
        this.keys = this.keys.filter(key => wanted.has(key));
        for (const key of keys) if (!this.keys.includes(key)) this.keys.push(key);
        if (this.focused && !wanted.has(this.focused)) this.focused = null;
        if (this.keys.length < 2 && this.layout === "focus" && !this.focused) this.layout = "grid";
        return this;
    }

    /** Tap on a tile: focus it, or back to the grid when it already has focus. */
    tap(key) {
        if (!this.keys.includes(key)) return this;
        if (this.layout === "focus" && this.focusKey === key) {
            this.layout = "grid";
            this.focused = null;
        } else {
            this.layout = "focus";
            this.focused = key;
        }
        return this;
    }

    setLayout(layout) {
        this.layout = layout === "focus" ? "focus" : "grid";
        if (this.layout === "grid") this.focused = null;
        return this;
    }

    /** The large tile in focus layout. */
    get focusKey() {
        if (this.layout !== "focus") return null;
        return this.focused && this.keys.includes(this.focused) ? this.focused : this.keys[0] ?? null;
    }

    /** Exchanges two tiles; in focus layout the other one takes focus. */
    swap(a, b) {
        const i = this.keys.indexOf(a);
        const j = this.keys.indexOf(b);
        if (i < 0 || j < 0 || i === j) return this;
        [this.keys[i], this.keys[j]] = [this.keys[j], this.keys[i]];
        if (this.layout === "focus") {
            if (this.focusKey === a) this.focused = b;
            else if (this.focusKey === b) this.focused = a;
        }
        return this;
    }

    /** Small tiles of the focus layout, in order. */
    get others() {
        const focus = this.focusKey;
        return this.keys.filter(key => key !== focus);
    }

    /** Volume per stream: focus plays at full volume, the others ducked. */
    volume(key) {
        const focus = this.focusKey;
        return !focus || key === focus ? 1 : DUCKED_VOLUME;
    }
}

// --- Camera -------------------------------------------------------------------------

/** getUserMedia video constraints within the server's media limits. */
export function cameraConstraints(facingMode = "user", limits = null, { width = 640, height = 480, fps = 15 } = {}) {
    const bounded = limits?.video_max_width > 0 && limits?.video_max_height > 0;
    const scale = bounded ? Math.min(1, limits.video_max_width / width, limits.video_max_height / height) : 1;
    return {
        facingMode: { ideal: facingMode },
        width: { ideal: Math.max(1, Math.floor(width * scale)), ...(bounded ? { max: limits.video_max_width } : {}) },
        height: { ideal: Math.max(1, Math.floor(height * scale)), ...(bounded ? { max: limits.video_max_height } : {}) },
        frameRate: { ideal: fps },
    };
}

/** Caps an RTCRtpSender to the server's bitrate ceiling (15% RTP headroom like the desktop). */
export async function capSender(sender, limits) {
    if (!sender?.getParameters || !(limits?.video_max_bitrate > 0)) return;
    const parameters = sender.getParameters();
    if (!parameters.encodings?.length) parameters.encodings = [{}];
    for (const encoding of parameters.encodings) encoding.maxBitrate = Math.floor(limits.video_max_bitrate * 0.85);
    await sender.setParameters(parameters).catch(() => {});
}

/** Whether the server caps camera video at all. */
export function limited(limits) {
    return !!(limits && (limits.video_max_bitrate > 0 || limits.video_max_width > 0));
}

export const EFFECTS = ["none", "blur"];

/**
 * A camera that can switch front/back and run a full-frame privacy blur.
 * Segmentation (the desktop's MediaPipe person mask) would add ~13 MB of
 * WebAssembly to a phone app, so "blur" softens the whole picture: useful
 * to hide the room, but it also blurs the face. `track` is what to send;
 * it changes on flip without an effect, so callers replaceTrack on
 * "track" events.
 */
export class CameraSource extends EventTarget {
    constructor({ facingMode = "user", effect = "none", blurStrength = 12, limits = null } = {}) {
        super();
        this.facingMode = facingMode;
        this.effect = EFFECTS.includes(effect) ? effect : "none";
        this.blurStrength = blurStrength;
        this.limits = limits;
        this.raw = null;
        this.stream = null;
        this.#pipeline = null;
    }

    #pipeline;

    get track() {
        return this.stream?.getVideoTracks()[0] || null;
    }

    async start() {
        this.raw = await navigator.mediaDevices.getUserMedia({ video: cameraConstraints(this.facingMode, this.limits) });
        this.#output();
        return this.stream;
    }

    #output() {
        const previous = this.track;
        if (this.effect === "blur") {
            this.#pipeline ||= new BlurPipeline();
            this.#pipeline.source(this.raw, this.blurStrength);
            this.stream = this.#pipeline.stream;
        } else {
            this.#pipeline?.stop();
            this.#pipeline = null;
            this.stream = this.raw;
        }
        if (previous !== this.track) this.dispatchEvent(new CustomEvent("track", { detail: this.track }));
    }

    /** Switches between the front and the back camera. */
    async flip() {
        const next = this.facingMode === "user" ? "environment" : "user";
        // Many phones cannot open both cameras at once: release first.
        for (const track of this.raw?.getTracks() || []) track.stop();
        try {
            this.raw = await navigator.mediaDevices.getUserMedia({ video: cameraConstraints(next, this.limits) });
            this.facingMode = next;
        } catch (error) {
            this.raw = await navigator.mediaDevices.getUserMedia({ video: cameraConstraints(this.facingMode, this.limits) });
            this.#output();
            throw error;
        }
        this.#output();
        return this.facingMode;
    }

    setEffect(effect, blurStrength = this.blurStrength) {
        this.effect = EFFECTS.includes(effect) ? effect : "none";
        this.blurStrength = blurStrength;
        if (this.raw) this.#output();
    }

    stop() {
        this.#pipeline?.stop();
        this.#pipeline = null;
        for (const track of this.raw?.getTracks() || []) track.stop();
        this.raw = null;
        this.stream = null;
    }
}

/** Draws the camera into a canvas with a blur filter and captures it. */
class BlurPipeline {
    constructor() {
        this.video = Object.assign(document.createElement("video"), { muted: true, playsInline: true, autoplay: true });
        this.canvas = document.createElement("canvas");
        this.context = this.canvas.getContext("2d");
        // Safari before 18 ignores ctx.filter: blur by down- and up-scaling.
        this.filter = "filter" in this.context;
        this.small = this.filter ? null : document.createElement("canvas");
        this.stream = this.canvas.captureStream(15);
        this.timer = setInterval(() => this.draw(), 1000 / 15);
    }

    source(stream, strength) {
        this.strength = Math.max(2, Math.min(30, Number(strength) || 12));
        this.video.srcObject = stream;
        this.video.play().catch(() => {});
        const settings = stream.getVideoTracks()[0]?.getSettings?.() || {};
        this.canvas.width = settings.width || 640;
        this.canvas.height = settings.height || 480;
    }

    draw() {
        const { video, canvas, context } = this;
        if (video.readyState < 2) return;
        if (video.videoWidth && (canvas.width !== video.videoWidth || canvas.height !== video.videoHeight)) {
            canvas.width = video.videoWidth;
            canvas.height = video.videoHeight;
        }
        if (this.filter) {
            context.filter = `blur(${this.strength}px)`;
            context.drawImage(video, 0, 0, canvas.width, canvas.height);
            context.filter = "none";
            return;
        }
        const factor = Math.max(4, this.strength);
        this.small.width = Math.max(1, Math.round(canvas.width / factor));
        this.small.height = Math.max(1, Math.round(canvas.height / factor));
        this.small.getContext("2d").drawImage(video, 0, 0, this.small.width, this.small.height);
        context.imageSmoothingEnabled = true;
        context.drawImage(this.small, 0, 0, canvas.width, canvas.height);
    }

    stop() {
        clearInterval(this.timer);
        for (const track of this.stream.getTracks()) track.stop();
        this.video.srcObject = null;
    }
}

// --- Call waiting -------------------------------------------------------------------

/**
 * What an update of `call` means for this session while `current` may be
 * in progress: "follow" (it is the current call or there is none),
 * "waiting" (a second call rings), "waiting-ended" (the waiting call stopped
 * ringing) or "ignore".
 */
export function classifyCall(current, call, uid, waiting = null) {
    const me = call?.participants?.find(p => p.unique_id === uid);
    const ringing = !call?.ended_at && me?.state === "ringing" && call?.caller !== uid;
    const currentActive = !!current && !current.ended_at && ["ringing", "accepted"].includes(current.participants?.find(p => p.unique_id === uid)?.state);
    if (!current || current.id === call.id || !currentActive) return waiting?.id === call.id && !ringing ? "waiting-ended" : "follow";
    if (ringing) return "waiting";
    return waiting?.id === call.id ? "waiting-ended" : "ignore";
}

/** The other party of a call, for history: the caller, or the first callee. */
export function callPeer(call, uid) {
    if (!call) return "";
    if (call.caller !== uid) return call.caller;
    return call.participants?.find(p => p.unique_id !== uid)?.unique_id || "";
}

/**
 * How a call ended for this session: "completed" when it was connected,
 * "declined"/"missed" for a callee that did not answer, "cancelled" or
 * "unanswered" for a caller nobody picked up.
 */
export function callOutcome(call, uid, connected) {
    if (connected) return "completed";
    const me = call?.participants?.find(p => p.unique_id === uid);
    if (call?.caller === uid) return call.participants?.some(p => p.unique_id !== uid && p.state === "declined") ? "declined" : "unanswered";
    if (me?.state === "declined") return "declined";
    if (me?.state === "elsewhere") return "elsewhere";
    return "missed";
}
