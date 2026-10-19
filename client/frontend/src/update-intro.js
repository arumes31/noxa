// update-intro.js — C20: the first start after an update restart. The noXa
// mark converges from packets, slides left and the wordmark appears; no
// server name, no status text. Any click or key skips it.
import { createLogoMotion, prefersReducedMotion } from "./logo-motion.js";
import "./connect-splash.css";

const INTRO_MS = 4500;
const LEAVE_MS = 260;

export function playUpdateIntro({ duration = INTRO_MS } = {}) {
    const overlay = document.createElement("div");
    overlay.className = "update-intro";
    overlay.setAttribute("aria-hidden", "true");
    overlay.innerHTML = `
        <div class="update-intro-stage">
            <canvas class="update-intro-logo"></canvas>
            <p class="update-intro-word"><span>n</span><span>o</span><span>X</span><span>a</span></p>
        </div>`;
    document.body.appendChild(overlay);
    const motion = createLogoMotion(overlay.querySelector("canvas"), { choreography: "converge" });
    let done = false, timer = 0;
    const events = ["pointerdown", "keydown"];
    const finish = () => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        for (const name of events) window.removeEventListener(name, finish, true);
        const remove = () => { motion.dispose(); overlay.remove(); };
        if (prefersReducedMotion()) { remove(); return; }
        overlay.classList.add("leaving");
        setTimeout(remove, LEAVE_MS);
    };
    for (const name of events) window.addEventListener(name, finish, true);
    timer = setTimeout(finish, prefersReducedMotion() ? Math.min(duration, 1200) : duration);
    return { element: overlay, finish };
}
