// presence.js — status of members in the tree, and automatic "away" after
// a period without use. Status is per session on the server: this phone's
// status is its own session's, restored after every reconnect.

export const STATUSES = Object.freeze(["online", "away", "busy", "invisible"]);

/** A client's status; servers leave it empty for online. */
export function statusOf(client) {
    return STATUSES.includes(client?.status) ? client.status : "online";
}

/**
 * The presence of an account: its sessions in the tree, the most
 * available status among them, the status message and whether it is on
 * a phone only. Returns {online: false} when no session is visible.
 */
export function presenceOf(session, uid) {
    const clients = (session?.clients() || []).filter(c => c.unique_id === uid);
    if (!clients.length) return { online: false, status: "offline", message: "", onMobile: false, clients };
    const rank = { online: 0, busy: 1, away: 2, invisible: 3 };
    const best = [...clients].sort((a, b) => rank[statusOf(a)] - rank[statusOf(b)])[0];
    return {
        online: true, status: statusOf(best), message: best.status_message || "",
        onMobile: clients.every(c => c.on_mobile), clients, client: best,
    };
}

/**
 * Sets "away" after minutes() without activity, and restores the previous
 * status on the next activity. The clock and timers are injected so the
 * behaviour is tested without waiting. apply(status, message) talks to the
 * server; current() is the status the user chose.
 */
export class AutoAway {
    constructor({ minutes, message = () => "", current, apply, now = () => Date.now(), setTimer = setTimeout, clearTimer = clearTimeout }) {
        Object.assign(this, { minutes, message, current, apply, now, setTimer, clearTimer });
        this.last = now();
        this.away = false;
        this.timer = 0;
        this.schedule();
    }

    get limit() {
        return Math.max(0, Number(this.minutes()) || 0) * 60000;
    }

    /** The user did something (or the app came back to the foreground). */
    activity() {
        this.last = this.now();
        if (this.away) {
            this.away = false;
            const chosen = this.current();
            this.apply(chosen.status, chosen.message);
        }
        this.schedule();
    }

    schedule() {
        this.clearTimer(this.timer);
        this.timer = 0;
        if (!this.limit || this.away) return;
        this.timer = this.setTimer(() => this.check(), Math.max(1000, this.last + this.limit - this.now()));
    }

    /** Goes away when the limit passed; otherwise waits for the rest. */
    check() {
        this.timer = 0;
        if (!this.limit || this.away) return;
        if (this.now() - this.last < this.limit) {
            this.schedule();
            return;
        }
        // Busy, away or invisible were chosen deliberately; only online idles.
        if (this.current().status !== "online") return;
        this.away = true;
        this.apply("away", this.message());
    }

    stop() {
        this.clearTimer(this.timer);
        this.timer = 0;
    }
}
