// Native GIF images have no pause API. Replace them with a static preview while
// suspended and retain the original bytes for playback and the full-size viewer.
const originals = new WeakMap();
export const GIF_BACKGROUND_DELAY = 60_000;

export function originalGIFSource(image) {
    return originals.get(image) || image.src;
}

export function manageChatGIFPlayback(root, selector = "img") {
    const images = new Map();
    const events = new AbortController();
    let background = document.hidden || !document.hasFocus();
    let suspended = false, timer = null;

    function pause(image, state) {
        if (state.paused || !image.complete || !image.naturalWidth) return;
        const canvas = document.createElement("canvas");
        const scale = Math.min(1, 1024 / Math.max(image.naturalWidth, image.naturalHeight));
        canvas.width = Math.max(1, Math.round(image.naturalWidth * scale));
        canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
        try {
            const context = canvas.getContext("2d");
            if (!context) return;
            context.drawImage(image, 0, 0, canvas.width, canvas.height);
            const still = canvas.toDataURL("image/png");
            // Preserve the intrinsic dimensions when the bounded preview is smaller.
            if (!image.hasAttribute("width") && !image.hasAttribute("height")) {
                image.width = image.naturalWidth;
                image.height = image.naturalHeight;
            }
            originals.set(image, state.source);
            state.paused = true;
            image.src = still;
        } catch {
            // A failed decode cannot replace a usable image with an empty frame.
        }
    }

    function update(image, state) {
        if (suspended) pause(image, state);
        else if (!background && state.visible && state.paused) {
            state.paused = false;
            image.src = state.source;
        }
    }

    const visibility = new IntersectionObserver(entries => {
        for (const entry of entries) {
            const state = images.get(entry.target);
            if (!state) continue;
            state.visible = entry.isIntersecting && entry.intersectionRect.width > 0 && entry.intersectionRect.height > 0;
            update(entry.target, state);
        }
    });

    function register(image) {
        if (images.has(image) || !image.src.startsWith("data:image/gif;base64,")) return;
        const state = { source: image.src, visible: false, paused: false };
        images.set(image, state);
        visibility.observe(image);
        update(image, state);
    }

    function scan(node) {
        if (node.nodeType !== 1) return;
        if (node.matches(selector)) register(node);
        node.querySelectorAll(selector).forEach(register);
    }

    const changes = new MutationObserver(records => {
        for (const record of records) for (const node of record.addedNodes) scan(node);
        for (const image of images.keys()) if (!root.contains(image)) {
            visibility.unobserve(image);
            images.delete(image);
        }
    });
    changes.observe(root, { childList: true, subtree: true });
    scan(root);
    root.addEventListener("load", event => {
        const state = images.get(event.target);
        if (state) update(event.target, state);
    }, { capture: true, signal: events.signal });

    function beginBackgroundDelay() {
        timer = setTimeout(() => {
            timer = null;
            if (!background) return;
            suspended = true;
            for (const [image, state] of images) update(image, state);
        }, GIF_BACKGROUND_DELAY);
    }

    function focusChanged() {
        const next = document.hidden || !document.hasFocus();
        if (next === background) return;
        background = next;
        clearTimeout(timer); timer = null;
        if (background) beginBackgroundDelay();
        else {
            suspended = false;
            for (const [image, state] of images) update(image, state);
        }
    }
    window.addEventListener("blur", focusChanged, { signal: events.signal });
    window.addEventListener("focus", focusChanged, { signal: events.signal });
    document.addEventListener("visibilitychange", focusChanged, { signal: events.signal });
    if (background) beginBackgroundDelay();
    return () => {
        clearTimeout(timer);
        events.abort(); changes.disconnect(); visibility.disconnect(); images.clear();
    };
}
