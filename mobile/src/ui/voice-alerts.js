// voice-alerts.js — voice notices that matter on every screen: someone
// whispers to you, you talk while muted, a moderator calls on your hand, the
// microphone fails. Attached once per Voice (a reconnect creates a new one).
import { app } from "../app.js";
import { t } from "../i18n.js";
import { toast } from "./dom.js";
import "./voice-strings.js";

const attached = new WeakSet();
/** Whisper toasts repeat at most this often per whisperer. */
const WHISPER_TOAST_MS = 15000;

function attach(voice) {
    if (!voice || attached.has(voice)) return;
    attached.add(voice);
    const lastToast = new Map();
    voice.addEventListener("whisper", event => {
        const data = event.detail;
        if (!data.speaking) return;
        const now = Date.now();
        if (now - (lastToast.get(data.from_client_id) || 0) < WHISPER_TOAST_MS) return;
        lastToast.set(data.from_client_id, now);
        toast(t("vc.whisperIncoming", { name: data.from_nickname || t("dm.someone") }));
        navigator.vibrate?.(60);
    });
    voice.addEventListener("muted-talking", () => toast(t("vc.mutedTalking"), "warn"));
    voice.addEventListener("called", () => {
        toast(t("vc.called"));
        navigator.vibrate?.([120, 80, 120]);
    });
    voice.addEventListener("mic-error", event => toast(t("vc.micError", { error: event.detail?.message || String(event.detail) }), "warn"));
}

attach(app.voice);
app.addEventListener("session", () => attach(app.voice));
