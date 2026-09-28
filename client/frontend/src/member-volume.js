import { getUserVolume, setUserVolume, previewUserVolume, clearUserVolumePreview, onUserVolumeChange } from "./audio.js";
import { t } from "./i18n.js";

export function bindMemberVolume(slider, output, reset, uid, { format = value => `${value}%`, onError, onSave } = {}) {
    const owner = Symbol("volume preview");
    let saving = false;
    let editing = false;
    const show = () => { output.textContent = format(slider.value); slider.setAttribute("aria-valuetext", output.textContent); };
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
        onSave?.();
        try { await setUserVolume(uid, Number(slider.value)); }
        catch (error) { if (slider.isConnected) { if (onError) onError(error); else window.__noxa.toast(String(error), "error"); } }
        finally {
            saving = false; slider.disabled = reset.disabled = false; restore();
            if ((focused === slider || focused === reset) && focused.isConnected && document.activeElement === document.body) focused.focus();
        }
    };
    reset.onclick = () => { slider.value = "100"; slider.oninput(); void slider.onchange(); };
    const unsubscribe = onUserVolumeChange(changedUID => { if (changedUID === uid) refresh(); });
    const dispose = () => { unsubscribe(); restore(); };
    dispose.refresh = refresh;
    dispose.cancel = restore;
    return dispose;
}
