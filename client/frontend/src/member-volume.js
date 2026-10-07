import { getUserVolume, setUserVolume, previewUserVolume, clearUserVolumePreview, onUserVolumeChange, personalVolumeGain } from "./audio.js";
import { t } from "./i18n.js";
import { getUserShareVolume, setUserShareVolume, onShareAudioChange } from "./audio.js";

export function bindMemberShareVolume(slider, output, reset, uid) {
    let saving = false, editing = false;
    const show = () => showVolume(slider, output, `${slider.value}%`);
    const refresh = () => { if (!saving && !editing) { slider.value = Math.round(getUserShareVolume(uid) * 100); show(); } };
    slider.setAttribute("aria-label", t("context.shareVolume"));
    slider.oninput = () => { editing = true; show(); };
    slider.onpointercancel = () => { editing = false; refresh(); };
    slider.onchange = async () => {
        if (saving) return;
        const focused = document.activeElement;
        saving = true; slider.disabled = reset.disabled = true;
        if (output.tagName === "INPUT") output.disabled = true;
        try { await setUserShareVolume(uid, Number(slider.value)); }
        catch (error) { if (slider.isConnected) window.__noxa.toast(String(error), "error"); }
        finally {
            saving = editing = false; slider.disabled = reset.disabled = false; refresh();
            if ((focused === slider || focused === reset || focused === output) && focused.isConnected && document.activeElement === document.body) focused.focus();
        }
    };
    reset.onclick = () => { slider.value = "100"; show(); void slider.onchange(); };
    refresh();
    return onShareAudioChange(changedUID => { if (!changedUID || changedUID === uid) refresh(); });
}

export function bindMemberVolume(slider, output, reset, uid, { format = value => `${value}%`, onError, onSave } = {}) {
    const owner = Symbol("volume preview");
    let saving = false;
    let editing = false;
    const show = () => showVolume(slider, output, format(slider.value));
    const refresh = () => { if (!saving && !editing) { slider.value = Math.round(getUserVolume(uid) * 100); show(); } };
    const restore = () => { editing = false; clearUserVolumePreview(uid, owner); refresh(); };
    restore();
    slider.setAttribute("aria-label", t("context.personalVolume"));
    slider.oninput = () => { if (saving) return; editing = true; previewUserVolume(uid, slider.value, owner); show(); };
    slider.onpointercancel = restore;
    slider.onchange = async () => {
        if (saving) return;
        const focused = document.activeElement;
        saving = true; slider.disabled = reset.disabled = true;
        if (output.tagName === "INPUT") output.disabled = true;
        onSave?.();
        try { await setUserVolume(uid, Number(slider.value)); }
        catch (error) { if (slider.isConnected) { if (onError) onError(error); else window.__noxa.toast(String(error), "error"); } }
        finally {
            saving = false; slider.disabled = reset.disabled = false; restore();
            if ((focused === slider || focused === reset || focused === output) && focused.isConnected && document.activeElement === document.body) focused.focus();
        }
    };
    reset.onclick = () => { slider.value = "100"; slider.oninput(); void slider.onchange(); };
    const unsubscribe = onUserVolumeChange(changedUID => { if (!changedUID || changedUID === uid) refresh(); });
    const dispose = () => { unsubscribe(); restore(); };
    dispose.refresh = refresh;
    dispose.cancel = restore;
    return dispose;
}

function showVolume(slider, output, text) {
    const amplified = Number(slider.value) > 100;
    const group = slider.closest(".ctx-audio-group");
    // Segments are decorative: the fill and native thumb follow every 1% step.
    group?.style.setProperty("--volume-position", `${Number(slider.value) / 2}%`);
    const decibels = group?.querySelector(".ctx-audio-db");
    const gain = personalVolumeGain(Number(slider.value) / 100);
    const db = gain > 0 ? Number((20 * Math.log10(gain)).toFixed(1)) : -Infinity;
    const gainLabel = Number.isFinite(db) ? `${db > 0 ? "+" : ""}${db} dB` : "−∞ dB";
    const label = amplified || decibels ? `${text} · ${gainLabel}` : text;
    if (decibels) decibels.textContent = gainLabel;
    if (output.tagName === "INPUT") { output.value = slider.value; output.disabled = slider.disabled; }
    else output.textContent = label;
    output.title = label;
    group?.classList.toggle("amplified", amplified);
    slider.setAttribute("aria-valuetext", label);
}
