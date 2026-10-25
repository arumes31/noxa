// undo-send.js — the "Undo send" delay (desktop chat-delivery.js). A held
// message waits in the outbox (so a closed app still sends it on its next
// start) while a local timer counts down; Undo cancels it before it leaves.
// The clock and timers are injectable so the countdown can be tested.

export class DelayedSends extends EventTarget {
    #entries = new Map(); // id -> {id, dueAt, timer, fire}

    constructor({ now = () => Date.now(), setTimer = (fn, ms) => setTimeout(fn, ms), clearTimer = id => clearTimeout(id) } = {}) {
        super();
        this.now = now;
        this.setTimer = setTimer;
        this.clearTimer = clearTimer;
    }

    /** Holds id for seconds, then calls fire(). Returns {id, dueAt}. */
    hold(id, seconds, fire) {
        this.cancel(id);
        const dueAt = this.now() + Math.max(0, seconds) * 1000;
        const entry = { id, dueAt, fire };
        entry.timer = this.setTimer(() => this.#fire(id), dueAt - this.now());
        this.#entries.set(id, entry);
        this.#changed();
        return { id, dueAt };
    }

    has(id) {
        return this.#entries.has(id);
    }

    /** Whole seconds until id leaves, rounded up; 0 when not held. */
    remaining(id) {
        const entry = this.#entries.get(id);
        return entry ? Math.max(0, Math.ceil((entry.dueAt - this.now()) / 1000)) : 0;
    }

    /** Cancels a held message. Returns whether it was still waiting. */
    cancel(id) {
        const entry = this.#entries.get(id);
        if (!entry) return false;
        this.clearTimer(entry.timer);
        this.#entries.delete(id);
        this.#changed();
        return true;
    }

    /** Ids still waiting. */
    pending() {
        return [...this.#entries.keys()];
    }

    #fire(id) {
        const entry = this.#entries.get(id);
        if (!entry) return;
        this.#entries.delete(id);
        this.#changed();
        entry.fire?.();
    }

    #changed() {
        this.dispatchEvent(new CustomEvent("change"));
    }
}

/** The delays the settings offer, in seconds. */
export const UNDO_SEND_CHOICES = [0, 3, 5, 10];
