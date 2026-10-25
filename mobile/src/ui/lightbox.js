// lightbox.js — a full-screen view of an image attachment. Pinch zoom is
// the browser's own (touch-action allows it on the image); a double tap or
// the zoom button toggles 2x. Swipe down, the close button or Escape close
// it. Share hands the file to the system sheet when the browser can, else
// it downloads.
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import "./rich-strings.js";
import "./lightbox.css";

export function openLightbox({ url, blob, name }) {
    const opener = document.activeElement;
    const image = h("img", { class: "lightbox-image", src: url, alt: name });
    const stage = h("div", { class: "lightbox-stage" }, image);
    let zoomed = false;
    const zoom = () => {
        zoomed = !zoomed;
        stage.classList.toggle("zoomed", zoomed);
        zoomButton.setAttribute("aria-pressed", String(zoomed));
    };
    const zoomButton = h("button", { class: "icon-btn", type: "button", "aria-label": t("rich.zoom"), "aria-pressed": "false", onclick: zoom }, "⤢");
    const close = () => {
        overlay.remove();
        document.removeEventListener("keydown", onKey);
        opener?.focus?.();
    };
    const onKey = event => { if (event.key === "Escape") close(); };
    const share = async () => {
        try {
            const file = blob && new File([blob], name, { type: blob.type });
            if (file && navigator.canShare?.({ files: [file] })) {
                await navigator.share({ files: [file], title: name });
                return;
            }
        } catch (error) {
            if (error?.name === "AbortError") return;
        }
        const link = h("a", { href: url, download: name });
        document.body.append(link);
        link.click();
        link.remove();
        toast(t("rich.saved", { name }));
    };
    const overlay = h("div", { class: "lightbox", role: "dialog", "aria-modal": "true", "aria-label": name },
        h("div", { class: "lightbox-bar" },
            h("button", { class: "icon-btn", type: "button", "aria-label": t("common.close"), onclick: close }, icon("close")),
            h("span", { class: "lightbox-name" }, name),
            zoomButton,
            h("button", { class: "icon-btn", type: "button", "aria-label": t("rich.shareImage"), onclick: share }, icon(navigator.share ? "share" : "download"))),
        stage);
    let lastTap = 0;
    image.addEventListener("click", () => {
        const now = Date.now();
        if (now - lastTap < 300) zoom();
        lastTap = now;
    });
    // A one-finger swipe down (while not zoomed) closes.
    let start = null;
    stage.addEventListener("touchstart", event => { start = event.touches.length === 1 && !zoomed ? event.touches[0].clientY : null; }, { passive: true });
    stage.addEventListener("touchmove", event => {
        if (start === null || event.touches.length !== 1) return;
        const dy = Math.max(0, event.touches[0].clientY - start);
        image.style.transform = dy ? `translateY(${dy}px)` : "";
        image.style.opacity = String(Math.max(0.3, 1 - dy / 400));
    }, { passive: true });
    stage.addEventListener("touchend", event => {
        if (start === null) return;
        const dy = (event.changedTouches[0]?.clientY ?? start) - start;
        start = null;
        if (dy > 120) close();
        else { image.style.transform = ""; image.style.opacity = ""; }
    });
    document.addEventListener("keydown", onKey);
    document.body.append(overlay);
    overlay.querySelector("button")?.focus();
    return close;
}
