// connect-splash.js — C04: shown over the login while a connection is made.
// The comet draws the noXa mark, "Connecting to <host>" names the address the
// user typed, and a random playful line changes every few seconds.
import { t } from "./i18n.js";
import { quipKey, shuffledQuips } from "./connect-messages.js";
import { createLogoMotion, prefersReducedMotion } from "./logo-motion.js";
import "./connect-splash.css";

const QUIP_MS = 2400;
const MIN_VISIBLE_MS = 3000;
const LEAVE_MS = 260;
let activeSplash = null;

export function showConnectSplash(host, { now = () => performance.now() } = {}) {
    activeSplash?.finish(false);
    const overlay = document.createElement("div");
    overlay.className = "connect-splash";
    overlay.setAttribute("role", "status");
    overlay.setAttribute("aria-live", "polite");
    overlay.innerHTML = `
        <canvas class="connect-splash-logo" aria-hidden="true"></canvas>
        <h2 class="connect-splash-title"></h2>
        <p class="connect-splash-quip"></p>`;
    overlay.querySelector(".connect-splash-title").textContent = t("connect.title", { host });
    const quip = overlay.querySelector(".connect-splash-quip");
    document.body.appendChild(overlay);
    const shownAt = now();
    const motion = createLogoMotion(overlay.querySelector("canvas"), { choreography: "trace", busy: true });
    let order = shuffledQuips(), position = 0, timer = 0, finished = false, removed = false;
    let leaveTimer = 0, removeTimer = 0;
    const nextQuip = () => {
        if (position >= order.length) { order = shuffledQuips(undefined, undefined, order[order.length - 1]); position = 0; }
        quip.textContent = t(quipKey(order[position++]));
        if (!prefersReducedMotion()) {
            quip.classList.remove("swap");
            void quip.offsetWidth;
            quip.classList.add("swap");
        }
    };
    nextQuip();
    timer = setInterval(nextQuip, QUIP_MS);
    const remove = () => {
        if (removed) return;
        removed = true;
        clearInterval(timer);
        clearTimeout(leaveTimer);
        clearTimeout(removeTimer);
        window.removeEventListener("pointerdown", remove, true);
        window.removeEventListener("keydown", remove, true);
        motion.dispose(); overlay.remove();
        if (activeSplash?.element === overlay) activeSplash = null;
    };
    // The decoration must not obscure a user moving on to another task.
    window.addEventListener("pointerdown", remove, true);
    window.addEventListener("keydown", remove, true);
    const splash = {
        element: overlay,
        // A successful connection fades the splash out once it has been seen;
        // a failure removes it at once so the login error is visible.
        finish(success) {
            if (!success) { remove(); return; }
            if (finished || removed) return;
            finished = true;
            clearInterval(timer);
            motion.settle();
            if (prefersReducedMotion()) { remove(); return; }
            const wait = Math.max(0, MIN_VISIBLE_MS - (now() - shownAt));
            leaveTimer = setTimeout(() => {
                overlay.classList.add("leaving");
                removeTimer = setTimeout(remove, LEAVE_MS);
            }, wait);
        },
    };
    activeSplash = splash;
    return splash;
}
