// Shared lifecycle for pointer and keyboard context menus.
let active = null;

export function closeContextMenu(expected, restoreFocus = false) {
    if (!active || (expected && expected !== active.menu)) return;
    const owner = active;
    active = null;
    owner.events.abort();
    owner.menu.remove();
    owner.onClose?.();
    if (restoreFocus) {
        const target = owner.trigger?.isConnected ? owner.trigger : owner.resolveTrigger?.();
        if (target?.isConnected) target.focus();
    }
}

export function mountContextMenu(menu, { x, y, trigger = document.activeElement, resolveTrigger, onClose } = {}) {
    closeContextMenu();
    const events = new AbortController();
    active = { menu, trigger, resolveTrigger, events, onClose };
    menu.setAttribute("role", "menu");
    menu.tabIndex = -1;
    for (const action of menu.querySelectorAll("a, button")) {
        if (!action.onclick && !action.hasAttribute("data-act")) continue;
        action.setAttribute("role", "menuitem");
        action.tabIndex = -1;
        if (action.classList.contains("disabled") || action.disabled) action.setAttribute("aria-disabled", "true");
    }
    document.body.append(menu);
    const anchor = trigger?.getBoundingClientRect?.();
    const gap = 8;
    menu.style.maxWidth = `${Math.max(0, innerWidth - gap * 2)}px`;
    menu.style.maxHeight = `${Math.max(0, innerHeight - gap * 2)}px`;
    const bounds = menu.getBoundingClientRect();
    menu.style.left = `${Math.max(gap, Math.min(x ?? anchor?.left ?? gap, innerWidth - bounds.width - gap))}px`;
    menu.style.top = `${Math.max(gap, Math.min(y ?? anchor?.bottom ?? gap, innerHeight - bounds.height - gap))}px`;
    const items = () => [...menu.querySelectorAll('[role="menuitem"], input')].filter(item => !item.disabled && item.getAttribute("aria-disabled") !== "true" && !item.hidden);
    menu.addEventListener("click", event => event.stopPropagation(), { signal: events.signal });
    menu.addEventListener("keydown", event => {
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeContextMenu(menu, true); return; }
        if (event.key === "Tab") { closeContextMenu(menu, true); return; }
        // Left/Right adjust a range; Up/Down continue through menu actions.
        if (event.target.matches("input") && !(event.target.type === "range" && ["ArrowDown", "ArrowUp"].includes(event.key))) return;
        if (["Enter", " "].includes(event.key)) { event.preventDefault(); event.stopPropagation(); event.target.click(); return; }
        if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
        event.preventDefault(); event.stopPropagation();
        const entries = items(), index = entries.indexOf(document.activeElement);
        const next = event.key === "Home" ? 0 : event.key === "End" ? entries.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + entries.length) % entries.length;
        entries[next]?.focus();
    }, { signal: events.signal });
    document.addEventListener("pointerdown", event => { if (!menu.contains(event.target)) closeContextMenu(menu); }, { signal: events.signal, capture: true });
    window.addEventListener("resize", () => closeContextMenu(menu), { signal: events.signal });
    (items()[0] || menu).focus({ preventScroll: true });
}

export function contextMenuKey(event) {
    return event.key === "ContextMenu" || (event.shiftKey && event.key === "F10");
}
