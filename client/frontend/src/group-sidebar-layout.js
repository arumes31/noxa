import { t } from "./i18n.js";

const storageKey = "noxa.group-sidebar-layout";
const clamp = value => Math.max(20, Math.min(80, value));

export function initGroupSidebarLayout({ tree, body, title }) {
    let size = 60, collapsed = false, automatic = true;
    try {
        const saved = JSON.parse(localStorage.getItem(storageKey));
        if (Number.isFinite(saved?.size)) { size = clamp(saved.size); automatic = false; }
        collapsed = saved?.collapsed === true;
    } catch { /* Layout remains usable when browser storage is unavailable. */ }
    const split = tree.parentElement;
    split.classList.add("group-split");
    const divider = document.createElement("div");
    divider.className = "group-divider"; divider.tabIndex = 0;
    divider.setAttribute("role", "separator");
    divider.setAttribute("aria-orientation", "horizontal");
    divider.setAttribute("aria-controls", tree.id);
    divider.setAttribute("aria-valuemin", "20"); divider.setAttribute("aria-valuemax", "80");
    tree.after(divider);
    const toggle = document.createElement("button");
    toggle.type = "button"; toggle.className = "group-collapse";
    toggle.setAttribute("aria-controls", body.id);
    title.replaceWith(toggle); toggle.append(title);
    let pointer = null;
    const save = () => {
        try { localStorage.setItem(storageKey, JSON.stringify({ size: automatic ? null : size, collapsed })); }
        catch { /* Persistence is optional; the current layout still works. */ }
    };
    const apply = () => {
        split.style.setProperty("--channel-share", String(size));
        split.style.setProperty("--group-share", String(100 - size));
        split.classList.toggle("groups-collapsed", collapsed);
        split.classList.toggle("group-split-auto", automatic);
        body.hidden = collapsed; divider.hidden = collapsed;
        toggle.setAttribute("aria-expanded", String(!collapsed));
        divider.setAttribute("aria-valuemax", automatic ? "100" : "80");
        divider.setAttribute("aria-valuenow", String(Math.round(size)));
    };
    const expand = () => { collapsed = false; apply(); save(); };
    toggle.onclick = () => { collapsed = !collapsed; apply(); save(); };
    divider.onkeydown = event => {
        const next = { ArrowUp: size - 5, ArrowDown: size + 5, Home: 20, End: 80 }[event.key];
        if (next === undefined) return;
        event.preventDefault(); size = clamp(next); automatic = false; apply(); save();
    };
    divider.onpointerdown = event => {
        if (event.button !== 0 || !event.isPrimary) return;
        event.preventDefault(); pointer = event.pointerId;
        divider.setPointerCapture(pointer); divider.focus({ preventScroll: true });
        split.classList.add("group-split-dragging");
    };
    divider.onpointermove = event => {
        if (pointer !== event.pointerId) return;
        const bounds = split.getBoundingClientRect();
        const available = bounds.height - divider.offsetHeight;
        if (available <= 0) return;
        size = clamp((event.clientY - bounds.top - divider.offsetHeight / 2) / available * 100);
        automatic = false;
        apply();
    };
    const finish = () => {
        const id = pointer; pointer = null;
        split.classList.remove("group-split-dragging");
        if (id !== null && divider.hasPointerCapture(id)) divider.releasePointerCapture(id);
        if (id !== null) save();
    };
    divider.onpointerup = finish; divider.onpointercancel = finish; divider.onlostpointercapture = finish;
    const translate = () => {
        title.textContent = t("group.title");
        divider.setAttribute("aria-label", t("group.resize")); divider.title = t("group.resize");
    };
    const observer = new ResizeObserver(() => {
        if (!automatic || collapsed) return;
        const available = split.clientHeight - divider.offsetHeight;
        if (available <= 0) return;
        size = tree.getBoundingClientRect().height / available * 100;
        divider.setAttribute("aria-valuenow", String(Math.round(size)));
    });
    observer.observe(tree);
    translate(); apply();
    return {
        expand, translate,
        destroy() {
            observer.disconnect(); finish(); divider.remove();
            split.classList.remove("group-split", "groups-collapsed", "group-split-auto");
            split.style.removeProperty("--channel-share"); split.style.removeProperty("--group-share");
        },
    };
}
