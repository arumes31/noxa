import { animatedChatImageSource } from "./chat-animation-source.js";

const originals = new WeakMap();
export function originalAvatarSource(image) {
    const state = originals.get(image);
    return state && image.src === state.poster ? state.source : image.src;
}

const selector = "img[data-avatar-image]";
const fallback = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";
export const AVATAR_BACKGROUND_DELAY = 60_000;

// Avatar originals stay in state.avatars. Only these DOM images get posters;
// stopping the native image decoder requires replacing src, not a CSS rule.
export function manageAvatarPlayback(root) {
    const images = new Map(), events = new AbortController();
    let background = document.hidden || !document.hasFocus(), paused = false, timer = null, disposed = false;
    function posterFor(image) {
        if (!image.complete || !image.naturalWidth) return null;
        try {
            const canvas = document.createElement("canvas");
            const scale = Math.min(1, 256 / Math.max(image.naturalWidth, image.naturalHeight));
            canvas.width = Math.max(1, Math.round(image.naturalWidth * scale));
            canvas.height = Math.max(1, Math.round(image.naturalHeight * scale));
            canvas.getContext("2d").drawImage(image, 0, 0, canvas.width, canvas.height);
            const frame = canvas.toDataURL("image/png");
            return frame.length <= 350000 ? frame : null;
        } catch { return null; }
    }
    function loadPoster(image, state) {
        // At most one bounded detached decode for this original. It never stays
        // attached or keeps animating after load, timeout, replacement or dispose.
        const probe = new Image();
        const finish = () => { clearTimeout(timeout); probe.onload = null; probe.onerror = null; probe.src = ""; state.cancelLoad = null; };
        const timeout = setTimeout(finish, 5000);
        state.cancelLoad = finish;
        probe.onload = () => {
            if (!disposed && paused && images.get(image) === state && image.src === state.poster) {
                const poster = posterFor(probe);
                if (poster) { state.poster = poster; image.src = poster; }
            }
            finish();
        };
        probe.onerror = finish;
        probe.src = state.source;
    }
    function update(image, state) {
        if (image.src !== state.source && image.src !== state.poster) return;
        if (paused && !state.poster) {
            const poster = posterFor(image);
            state.poster = poster || fallback; image.src = state.poster;
            if (!poster) loadPoster(image, state);
        } else if (!paused && state.poster) {
            state.cancelLoad?.();
            if (image.src === state.poster) image.src = state.source;
            state.poster = null;
        }
    }
    function forget(image, state) {
        state?.cancelLoad?.();
        if (state?.poster && image.src === state.poster) image.src = state.source;
        images.delete(image); originals.delete(image);
    }
    function register(image) {
        if (!image.matches(selector)) { forget(image, images.get(image)); return; }
        const previous = images.get(image);
        if (previous && (image.src === previous.source || image.src === previous.poster)) { update(image, previous); return; }
        previous?.cancelLoad?.(); images.delete(image); originals.delete(image);
        const source = image.src;
        if (source.length > 350000 || !/^data:image\/(?:gif|webp|png);base64,/.test(source) || !animatedChatImageSource(source)) return;
        const state = { source, poster: null, cancelLoad: null }; images.set(image, state); originals.set(image, state); update(image, state);
    }
    function scan(node) {
        if (node.nodeType !== 1) return;
        if (node.matches(selector)) register(node);
        node.querySelectorAll(selector).forEach(register);
    }
    const changes = new MutationObserver(records => {
        for (const record of records) {
            if (record.type === "attributes") register(record.target);
            else for (const node of record.addedNodes) scan(node);
        }
        for (const image of images.keys()) if (!root.contains(image)) forget(image, images.get(image));
    });
    changes.observe(root, { childList: true, subtree: true, attributes: true, attributeFilter: ["src", "data-avatar-image"] });
    scan(root);
    function startDelay() { timer = setTimeout(() => { timer = null; if (!background) return; paused = true; for (const [image, state] of images) update(image, state); }, AVATAR_BACKGROUND_DELAY); }
    function focusChanged(event) {
        const next = document.hidden || (event.type === "blur" ? true : !document.hasFocus());
        if (next === background) return;
        background = next; clearTimeout(timer); timer = null;
        if (background) startDelay();
        else { paused = false; for (const [image, state] of images) { register(image); update(image, state); } }
    }
    window.addEventListener("blur", focusChanged, { signal: events.signal });
    window.addEventListener("focus", focusChanged, { signal: events.signal });
    document.addEventListener("visibilitychange", focusChanged, { signal: events.signal });
    if (background) startDelay();
    return () => {
        disposed = true; clearTimeout(timer); events.abort(); changes.disconnect(); paused = false;
        for (const [image, state] of images) forget(image, state);
        images.clear();
    };
}
