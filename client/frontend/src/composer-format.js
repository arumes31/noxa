// composer-format.js — formatting toolbar for selected composer text (619).
// The composer is plain text: buttons wrap the selection in the markdown the
// renderer already understands (markdown.js), so sent messages are unchanged
// for every client. Ctrl+B / Ctrl+I toggle bold and italic.
export const FORMATS = Object.freeze([
    { id: "bold", marker: "**", label: "B", key: "b" },
    { id: "italic", marker: "*", label: "I", key: "i" },
    { id: "underline", marker: "__", label: "U", key: "u" },
    { id: "strike", marker: "~~", label: "S" },
    { id: "code", marker: "`", label: "</>" },
]);

/**
 * Toggles marker around value[start, end). Returns { value, start, end } with
 * the selection kept on the same text, or null for an empty selection.
 */
export function toggleFormat(value, start, end, marker) {
    if (typeof value !== "string" || !marker || !(start < end)) return null;
    const m = marker.length;
    const exactly = (at, length) => value.slice(at, at + length) === marker &&
        value[at - 1] !== marker[0] && value[at + length] !== marker[0];
    // Markers just outside the selection: unwrap them.
    if (start >= m && exactly(start - m, m) && exactly(end, m)) {
        return { value: value.slice(0, start - m) + value.slice(start, end) + value.slice(end + m), start: start - m, end: end - m };
    }
    // Selection includes the markers: unwrap inside.
    const selected = value.slice(start, end);
    if (selected.length > 2 * m && selected.startsWith(marker) && selected.endsWith(marker) &&
        selected[m] !== marker[0] && selected[selected.length - m - 1] !== marker[0]) {
        return { value: value.slice(0, start) + selected.slice(m, -m) + value.slice(end), start, end: end - 2 * m };
    }
    return { value: value.slice(0, start) + marker + selected + marker + value.slice(end), start: start + m, end: end + m };
}

/** Shortcut format for a keydown (Ctrl/Cmd + B/I/U), or null. */
export function shortcutFormat(event) {
    if (!(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey) return null;
    return FORMATS.find(format => format.key && format.key === String(event.key).toLowerCase()) || null;
}

/**
 * Mounts the toolbar for input. translate(id) returns the localized name of a
 * format. Typing or deleting dispatches "input" so drafts and typing notices
 * stay in sync.
 */
export function mountComposerFormat(input, translate, doc = document) {
    const bar = doc.createElement("div");
    bar.className = "composer-format hidden";
    bar.setAttribute("role", "toolbar");
    bar.setAttribute("aria-label", translate("toolbar"));
    const apply = format => {
        const next = toggleFormat(input.value, input.selectionStart, input.selectionEnd, format.marker);
        if (!next) return;
        input.value = next.value;
        input.setSelectionRange(next.start, next.end);
        input.dispatchEvent(new Event("input", { bubbles: true }));
        sync();
    };
    for (const format of FORMATS) {
        const button = doc.createElement("button");
        button.type = "button";
        button.className = `composer-format-btn fmt-${format.id}`;
        button.textContent = format.label;
        const name = translate(format.id);
        button.title = name;
        button.setAttribute("aria-label", name);
        // Keep focus and selection in the composer.
        button.addEventListener("mousedown", event => event.preventDefault());
        button.addEventListener("click", () => { apply(format); input.focus(); });
        bar.appendChild(button);
    }
    const sync = () => {
        const selected = doc.activeElement === input && input.selectionStart < input.selectionEnd;
        bar.classList.toggle("hidden", !selected);
    };
    input.addEventListener("keydown", event => {
        const format = shortcutFormat(event);
        if (!format) return;
        event.preventDefault();
        apply(format);
    });
    for (const type of ["select", "keyup", "mouseup", "focus", "input"]) input.addEventListener(type, sync);
    input.addEventListener("blur", () => bar.classList.add("hidden"));
    return bar;
}
