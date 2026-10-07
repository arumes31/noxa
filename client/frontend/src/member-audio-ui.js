import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { isUserMuted, setUserMuted, isUserShareMuted, setUserShareMuted, onShareAudioChange } from "./audio.js";
import { bindMemberVolume, bindMemberShareVolume } from "./member-volume.js";
import { sessionUserID } from "./session-identity.js";
import "./member-audio-ui.css";

let activePopover = null;

export function openMemberAudio(client, { x, y, trigger = document.activeElement, resolveTrigger } = {}) {
    activePopover?.();
    const v = window.__noxa, state = v.state;
    const { serverGeneration, sessionGeneration, activeTabID, myClientID, myUniqueID } = state;
    const identity = sessionUserID(state);
    const current = () => v === window.__noxa && state === v.state &&
        serverGeneration === state.serverGeneration && sessionGeneration === state.sessionGeneration &&
        activeTabID === state.activeTabID && myClientID === state.myClientID &&
        myUniqueID === state.myUniqueID && identity === sessionUserID(state);
    const panel = document.createElement("section");
    panel.className = "member-audio-popover";
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-label", t("context.personalAudio"));
    panel.tabIndex = -1;
    const header = document.createElement("header");
    header.className = "member-audio-heading";
    const title = document.createElement("h3"); title.textContent = t("context.personalAudio");
    const close = document.createElement("button"); close.type = "button"; close.className = "ctx-audio-icon";
    close.innerHTML = icon("close"); close.setAttribute("aria-label", t("common.close"));
    header.append(title, close);
    const name = document.createElement("p"); name.className = "member-audio-name";
    name.textContent = client.nickname || client.unique_id;
    const controls = memberAudioControls(client, current);
    panel.append(header, name, controls.element);
    const events = new AbortController();
    let disposed = false;
    const dismiss = (restore = true) => {
        if (disposed) return;
        disposed = true;
        events.abort(); clearInterval(timer); observer.disconnect(); controls.dispose(); panel.remove();
        if (activePopover === dismiss) activePopover = null;
        if (restore && current()) {
            const target = trigger?.isConnected ? trigger : resolveTrigger?.();
            target?.focus({ preventScroll: true });
        }
    };
    // The check also runs before input handlers so stale controls cannot save
    // during the short interval between a session change and lifecycle cleanup.
    const guard = event => {
        if (current()) return;
        event?.preventDefault(); event?.stopImmediatePropagation(); dismiss(false);
    };
    for (const type of ["click", "input", "change", "keydown", "pointerdown"]) {
        panel.addEventListener(type, guard, { capture: true, signal: events.signal });
    }
    close.onclick = () => dismiss();
    document.addEventListener("keydown", event => {
        if (event.key !== "Escape") return;
        event.preventDefault(); event.stopPropagation(); dismiss();
    }, { capture: true, signal: events.signal });
    document.addEventListener("pointerdown", event => {
        if (!panel.contains(event.target)) dismiss();
    }, { capture: true, signal: events.signal });
    document.addEventListener("focusin", event => {
        if (!panel.contains(event.target)) dismiss(false);
    }, { signal: events.signal });
    window.addEventListener("resize", () => dismiss(), { signal: events.signal });
    const timer = setInterval(() => { if (!current()) dismiss(false); }, 150);
    const observer = new MutationObserver(() => { if (!panel.isConnected) dismiss(false); });
    document.body.append(panel);
    observer.observe(document.body, { childList: true });
    const anchor = trigger?.getBoundingClientRect?.();
    const bounds = panel.getBoundingClientRect(), gap = 8;
    panel.style.left = `${Math.max(gap, Math.min(x ?? anchor?.right ?? gap, innerWidth - bounds.width - gap))}px`;
    panel.style.top = `${Math.max(gap, Math.min(y ?? anchor?.top ?? gap, innerHeight - bounds.height - gap))}px`;
    activePopover = dismiss;
    panel.querySelector('input[type="range"]').focus({ preventScroll: true });
}

function memberAudioControls(client, current) {
    const v = window.__noxa, uid = client.unique_id;
    let disposed = false;
    const element = document.createElement("section"); element.className = "ctx-member-audio";
    const disposers = [];
    const group = share => {
        const host = document.createElement("fieldset"); host.className = `ctx-audio-group ${share ? "ctx-share-volume" : "ctx-volume"}`;
        const name = t(share ? "context.shareVolume" : "context.personalVolume");
        host.innerHTML = `<div class="ctx-audio-header"><button type="button" class="ctx-audio-icon" data-act="${share ? "mute-share" : "mute"}"></button><span class="ctx-audio-label"></span><label class="ctx-audio-percent"><input type="number" min="0" max="200" step="1" inputmode="numeric" /><span aria-hidden="true">%</span></label><output class="ctx-audio-db"></output><button type="button" class="ctx-audio-icon" data-act="${share ? "reset-share-volume" : "reset-volume"}">${icon("refresh")}</button></div><div class="ctx-audio-range"><input type="range" min="0" max="200" value="100" /><span class="ctx-audio-baseline" aria-hidden="true">100%</span><span class="ctx-audio-max" aria-hidden="true">+20 dB</span></div>`;
        host.querySelector(".ctx-audio-label").textContent = name;
        const slider = host.querySelector('input[type="range"]'), number = host.querySelector('input[type="number"]');
        number.setAttribute("aria-label", t("context.volumePercent", { name }));
        const reset = host.querySelector('[data-act^="reset"]'), mute = host.querySelector('[data-act^="mute"]');
        reset.title = reset.ariaLabel = t(share ? "context.shareVolumeReset" : "context.resetVolume");
        disposers.push(share ? bindMemberShareVolume(slider, number, reset, uid) : bindMemberVolume(slider, number, reset, uid));
        number.oninput = () => {
            if (number.value === "" || !number.checkValidity()) return;
            slider.value = number.value; slider.oninput();
        };
        number.onchange = () => {
            if (number.value === "" || !number.checkValidity()) { slider.onpointercancel(); number.value = slider.value; return; }
            slider.value = number.value; void slider.onchange();
        };
        number.onkeydown = event => { if (event.key === "Enter") { event.preventDefault(); number.onchange(); } };
        let savingMute = false;
        let renderedMute;
        const refreshMute = () => {
            const muted = share ? isUserShareMuted(uid) : isUserMuted(uid);
            // A volume input can save on blur between pointerdown and pointerup
            // on this icon. Replacing the unchanged SVG then cancels the click.
            if (renderedMute !== muted) {
                mute.innerHTML = icon(muted ? "speakerOff" : "speaker");
                renderedMute = muted;
            }
            mute.setAttribute("aria-pressed", String(muted));
            mute.title = mute.ariaLabel = t(share ? (muted ? "context.shareUnmute" : "context.shareMute") : (muted ? "context.voiceUnmute" : "context.voiceMute"));
            mute.disabled = savingMute;
            host.classList.toggle("muted", muted);
        };
        mute.onclick = async () => {
            if (savingMute || !current()) return;
            savingMute = true; refreshMute();
            try {
                if (share) await setUserShareMuted(uid, !isUserShareMuted(uid));
                else await setUserMuted(uid, !isUserMuted(uid));
                if (current()) v.renderTree?.();
            } catch (error) { if (current()) v.toast(String(error), "error"); }
            finally { savingMute = false; refreshMute(); if (mute.isConnected && document.activeElement === document.body) mute.focus(); }
        };
        disposers.push(onShareAudioChange(changed => { if (!changed || changed === uid) refreshMute(); }));
        refreshMute(); element.append(host);
        return host;
    };
    group(false);
    const share = group(true);
    const availability = document.createElement("p"); availability.className = "ctx-audio-unavailable";
    availability.textContent = t("context.noShareAudio"); availability.setAttribute("role", "status"); element.append(availability);
    const refreshAvailability = () => {
        const sessions = v.state.clients?.filter(member => member.unique_id === uid) || [];
        const available = current() && (client.client_id ? !!v.shareAudioCtl?.get(client.client_id) : sessions.some(member => v.shareAudioCtl?.get(member.client_id)));
        share.disabled = !available; availability.hidden = available;
    };
    refreshAvailability();
    const timer = setInterval(refreshAvailability, 500);
    for (const control of element.querySelectorAll("input, button")) {
        for (const property of ["oninput", "onchange", "onclick", "onkeydown", "onpointercancel"]) {
            const action = control[property];
            if (action) control[property] = event => { if (!disposed && current()) return action(event); };
        }
    }
    return { element, dispose: () => { disposed = true; clearInterval(timer); for (const dispose of disposers) dispose(); } };
}
