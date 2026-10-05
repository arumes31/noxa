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
            let pixels = "";
            try { pixels = canvas.toDataURL("image/png"); }
            catch {
                // Remote GIFs without CORS still allow drawing, but not reading
                // canvas pixels. Display that canvas over an inert image instead.
                const wrapper = document.createElement("span");
                wrapper.style.cssText = "display:inline-block;position:relative;max-width:100%;line-height:0";
                image.before(wrapper);
                wrapper.append(image, canvas);
                canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;pointer-events:none";
                canvas.setAttribute("aria-hidden", "true");
                state.wrapper = wrapper;
            }
            // This fixed SVG contains only our canvas PNG and numeric dimensions.
            // It preserves intrinsic sizing without forcing CSS width/height,
            // which would distort images constrained by max-width/max-height.
            const frame = `<svg xmlns="http://www.w3.org/2000/svg" width="${image.naturalWidth}" height="${image.naturalHeight}" viewBox="0 0 ${image.naturalWidth} ${image.naturalHeight}">${pixels ? `<image href="${pixels}" width="100%" height="100%"/>` : ""}</svg>`;
            const still = "data:image/svg+xml," + encodeURIComponent(frame);
            originals.set(image, state.source);
            state.paused = true;
            state.still = still;
            image.src = still;
        } catch {
            // A failed decode cannot replace a usable image with an empty frame.
        }
    }

    function update(image, state) {
        if (suspended || state.visible === false) pause(image, state);
        else if (!background && state.visible && state.paused) {
            state.paused = false;
            state.wrapper?.replaceWith(image);
            state.wrapper = null;
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
        if (!image.matches(selector)) return;
        const previous = images.get(image);
        if (previous && (image.src === previous.source || image.src === previous.still)) return;
        if (previous) {
            previous.wrapper?.replaceWith(image);
            originals.delete(image);
            visibility.unobserve(image);
            images.delete(image);
        }
        // Markdown embeds often use extensionless CDN URLs. Their animation
        // format is unknown, so freeze any remote image when playback is paused.
        if (!/^(?:data:image\/gif[;,]|https?:|blob:)/i.test(image.src)) return;
        const state = { source: image.src, visible: null, paused: false };
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
        for (const record of records) {
            if (record.type === "attributes") register(record.target);
            else for (const node of record.addedNodes) scan(node);
        }
        for (const image of images.keys()) if (!root.contains(image)) {
            visibility.unobserve(image);
            images.delete(image);
        }
    });
    changes.observe(root, { childList: true, subtree: true, attributes: true, attributeFilter: ["src"] });
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

    function focusChanged(event) {
        const next = document.hidden || (event.type === "blur" ? true : !document.hasFocus());
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
