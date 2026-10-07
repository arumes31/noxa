import { t } from "./i18n.js";

const measured = value => Number.isFinite(value) && value >= 0;
const recent = (sample, age) => measured(sample) && sample >= 1000 && sample <= 15000 && measured(age) && age <= 15000;
const atMost = (value, max) => measured(value) && value <= max;
const evidence = (key, value, unit) => ({ key, value, unit });

// These are hints from one stage's own interval, never an end-to-end bottleneck
// calculation. Server RTP timestamps and independently sampled frame rates must
// not be subtracted. Missing counters are unknown, including missing loss.
export function summarizeStreamHealth({ sender, senderAgeMS, receiver, receiverAgeMS,
    direction = receiver ? "receiver" : "sender", screen = false, waiting = false, starting = false } = {}) {
    const result = (state, side = direction, measurements = []) => ({ state, side,
        measurements: measurements.length ? [...measurements, evidence("sampleWindow", (side === "sender" ? sender.sample_ms : receiver.sampleMS) / 1000, "s")] : [] });
    if (waiting) return result("waiting");
    const s = recent(sender?.sample_ms, senderAgeMS) && sender.encoding_active !== false ? sender : null;
    // A receiver aggregate can contain rows with different intervals. Without
    // per-row evidence, it cannot establish decoder pressure for this stream.
    const r = recent(receiver?.sampleMS, receiverAgeMS) && (!receiver.rows || receiver.rows.length === 1) ? receiver : null;
    const senderActive = s && s.encoded_fps >= 5 && s.sent_fps >= 5;
    const senderWorking = s && measured(s.encoded_fps) && measured(s.sent_fps) &&
        [s.capture_fps, s.encoded_fps, s.sent_fps].some(value => measured(value) && value >= 5);
    const receiverActive = r && r.receivedFPS >= 5 && r.receivedFPS * r.sampleMS / 1000 >= 10;
    if (receiverActive && r.lossPercent >= 5 && r.nacks > 0) {
        return result("network", "receiver", [evidence("receiverLoss", r.lossPercent, "%"), evidence("repairRequests", r.nacks, "")]);
    }
    if (senderWorking && (s.bandwidth_limited_ms >= s.sample_ms / 2 || s.remote_fraction_lost >= 0.05 && s.retransmit_percent >= 5)) {
        return result("network", "sender", [evidence("bandwidthTime", s.bandwidth_limited_ms, "ms"),
            evidence("senderLoss", measured(s.remote_fraction_lost) ? s.remote_fraction_lost * 100 : null, "%")]);
    }
    if (receiverActive && r.fps > 0 && r.droppedFPS >= r.receivedFPS * 0.2 && r.decodeMS >= 1000 / r.receivedFPS && atMost(r.lossPercent, 1)) {
        return result("decoding", "receiver", [evidence("decodeTime", r.decodeMS, "ms"), evidence("droppedFrames", r.droppedFPS, "fps"), evidence("receiverLoss", r.lossPercent, "%")]);
    }
    if (senderWorking && s.cpu_limited_ms >= s.sample_ms / 2) {
        return result("encoding", "sender", [evidence("cpuTime", s.cpu_limited_ms, "ms"), evidence("encodeTime", s.encode_ms, "ms"), evidence("sentFrames", s.sent_fps, "fps")]);
    }
    // Low output is observable even without a reported limitation reason. The
    // capture window may differ, so this is not proof of an encoder bottleneck.
    if (s && s.capture_fps >= 10 && atMost(s.encoded_fps, 2) && atMost(s.sent_fps, 2)) {
        return result("lowOutput", "sender", [evidence("capturedFrames", s.capture_fps, "fps"),
            evidence("encodedFrames", s.encoded_fps, "fps"), evidence("sentFrames", s.sent_fps, "fps")]);
    }
    const senderClear = s && s.cpu_limited_ms === 0 && s.bandwidth_limited_ms === 0 && atMost(s.remote_fraction_lost, 0.01);
    if (direction === "sender" && screen && senderClear && atMost(s.capture_fps, 2) && atMost(s.encoded_fps, 2) && atMost(s.sent_fps, 2)) {
        return result("lowActivity", "sender", [evidence("capturedFrames", s.capture_fps, "fps"), evidence("sentFrames", s.sent_fps, "fps")]);
    }
    if (direction === "receiver" && receiverActive && r.fps >= r.receivedFPS * 0.95 && atMost(r.droppedFPS, r.receivedFPS * 0.05) &&
        atMost(r.lossPercent, 1) && atMost(r.decodeMS, 500 / r.receivedFPS) && r.freezes === 0) {
        return result("normal", "receiver", [evidence("decodedFrames", r.fps, "fps"), evidence("receiverLoss", r.lossPercent, "%")]);
    }
    if (direction === "sender" && senderActive && senderClear && s.sent_fps >= s.encoded_fps * 0.95 && atMost(s.encode_ms, 500 / s.encoded_fps)) {
        return result("normal", "sender", [evidence("sentFrames", s.sent_fps, "fps"), evidence("encodeTime", s.encode_ms, "ms")]);
    }
    if (starting) return result("starting");
    return result("unknown", direction, direction === "sender" && s ? [evidence("sentFrames", s.sent_fps, "fps"), evidence("encodeTime", s.encode_ms, "ms")]
        : direction === "receiver" && r ? [evidence("decodedFrames", r.fps, "fps"), evidence("receiverLoss", r.lossPercent, "%")] : []);
}

// Keep the live headline mounted and unchanged across numerical refreshes.
// Measurements and advice remain readable without repeated announcements.
export function renderStreamHealth(element, health) {
    if (!element.querySelector(".stream-health-headline")) {
        element.classList.add("stream-health");
        element.innerHTML = '<strong class="stream-health-headline" role="status" aria-live="polite" aria-atomic="true"></strong><p class="stream-health-evidence"></p><p class="stream-health-action"></p>';
    }
    element.dataset.health = health.state;
    const headline = element.querySelector(".stream-health-headline");
    const text = t(`streamHealth.${health.state}`);
    if (headline.textContent !== text) headline.textContent = text;
    const values = health.measurements.filter(item => measured(item.value)).map(item =>
        `${t(`streamHealth.measure.${item.key}`)}: ${item.value.toFixed(item.unit === "" ? 0 : 1)}${item.unit ? ` ${item.unit}` : ""}`);
    element.querySelector(".stream-health-evidence").textContent = values.length ? t("streamHealth.recent", { values: values.join(" · ") }) : "";
    element.querySelector(".stream-health-action").textContent = t(`streamHealth.action.${health.state}${["network", "encoding", "lowOutput"].includes(health.state) ? `.${health.side === "sender" && element.dataset.direction === "receiver" ? "remote" : health.side}` : ""}`);
}
