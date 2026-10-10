import { originalGIFSource } from "./chat-gif-playback.js";
import { closeDialog, mountServerDialog } from "./modal.js";

const expanded = new WeakMap();
const serverGeneration = () => Number(window.__noxa?.state?.serverGeneration || 0);

function moveVideo(parent, video, before = null) {
    const playing = !video.paused;
    // Atomic DOM moves preserve media state on supporting WebViews. The
    // fallback still uses one element and restores playback only if moving it
    // interrupted playback; it never auto-starts a manually paused clip.
    if (parent.moveBefore && parent.isConnected && video.isConnected) parent.moveBefore(video, before);
    else {
        parent.insertBefore(video, before);
        if (playing && video.paused) video.play()?.catch(() => {});
    }
}

export function openChatMediaLightbox(node) {
    if (!node?.isConnected || !["IMG", "VIDEO"].includes(node.tagName) || node.srcObject) return null;
    const existing = expanded.get(node);
    if (existing?.isConnected) return existing;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay lightbox";
    if (node.tagName === "IMG") {
        const big = node.cloneNode(true);
        big.src = originalGIFSource(node);
        big.removeAttribute("class"); big.removeAttribute("title");
        overlay.append(big);
        overlay.onclick = event => { if (event.target === overlay) closeDialog(overlay); };
        mountServerDialog(overlay);
        return overlay;
    }

    const generation = serverGeneration(), source = node.src;
    const placeholder = document.createElement("span"), box = node.getBoundingClientRect();
    const sourceStyle = getComputedStyle(node);
    placeholder.className = "chat-media-placeholder";
    placeholder.setAttribute("aria-hidden", "true");
    Object.assign(placeholder.style, {
        display: sourceStyle.display === "inline" ? "inline-block" : sourceStyle.display,
        boxSizing: "border-box", width: `${box.width}px`, height: `${box.height}px`,
        maxWidth: "100%", margin: sourceStyle.margin, verticalAlign: sourceStyle.verticalAlign,
    });
    node.before(placeholder);
    let finished = false;
    const observer = new MutationObserver(() => {
        if (!placeholder.isConnected || node.src !== source || generation !== serverGeneration()) closeDialog(overlay, "source-gone");
    });
    const restore = reason => {
        if (finished) return;
        finished = true;
        observer.disconnect(); expanded.delete(node);
        // A state-preserving move also preserves focus. Release native video
        // control focus before returning to an offscreen slot, otherwise the
        // browser can scroll the chat to reveal it behind the user's position.
        if (document.activeElement === node) node.blur();
        if (placeholder.isConnected && generation === serverGeneration() && node.src === source && !node.srcObject && reason !== "server-change") {
            moveVideo(placeholder.parentNode, node, placeholder);
        } else {
            if (!node.srcObject) node.pause();
            node.remove();
        }
        placeholder.remove();
    };
    overlay.onclick = event => {
        if (event.target !== overlay) return;
        restore("close"); closeDialog(overlay);
    };
    expanded.set(node, overlay);
    mountServerDialog(overlay, { onCancel: () => restore("cancel"), onClose: restore });
    moveVideo(overlay, node);
    observer.observe(document.body, { childList: true, subtree: true });
    observer.observe(node, { attributes: true, attributeFilter: ["src"] });
    return overlay;
}
