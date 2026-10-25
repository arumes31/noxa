// notifier.js — what the running app does when something happens: sounds
// and spoken announcements, in-app banners, the poke popup, the activity
// center's log, buddy and channel watches, automatic away and the push
// filters the server applies while the app is closed. app.js asks
// decide() for every incoming message; everything else follows app and
// session events.
import { ActivityLog } from "./core/activity.js";
import { channelEvent, decide, pushFilters } from "./core/notify-policy.js";
import { AutoAway } from "./core/presence.js";
import { setPushPreferences } from "./core/push.js";
import * as settings from "./core/settings.js";
import { PresenceWatch, isBlocked } from "./core/social.js";
import { EVENT_SOUNDS, SoundEngine } from "./core/sounds.js";
import { audioContext } from "./core/voice-audio.js";
import { lang, t } from "./i18n.js";
import { h, refreshAvatars, setAvatarSource } from "./ui/dom.js";
import "./ui/social-strings.js";
import "./ui/social.css";

const ROUTES = { channel: id => `#/chat/${id}`, dm: id => `#/dm/${encodeURIComponent(id)}`, conversation: id => `#/group/${encodeURIComponent(id)}` };

function routeOf(tag) {
    const split = tag.indexOf(":");
    return ROUTES[tag.slice(0, split)]?.(tag.slice(split + 1)) || "#/home";
}

const FILTER_SETTINGS = ["matrix", "channelNotify", "chatLevel", "dnd", "quietFrom", "quietTo", "snoozeUntil", "blockedUsers", "mutedConversations"];

class Notifier {
    constructor() {
        this.app = null;
        this.session = null;
        this.activity = null;
        this.watch = new PresenceWatch();
        this.away = null;
        this.sounds = new SoundEngine({
            // One AudioContext for the app: shared with voice playback (core/voice-audio.js).
            createContext: () => audioContext() || new (globalThis.AudioContext || globalThis.webkitAudioContext)(),
            load: async url => {
                const response = await fetch(url);
                if (!response.ok) throw new Error(`sound ${response.status}`);
                return response.arrayBuffer();
            },
            state: () => ({ enabled: settings.get("sounds"), volume: settings.get("soundVolume"), speech: settings.get("speech"), silenced: settings.silenced(), language: lang() }),
        });
    }

    /** Called once by app.js with the shared state. */
    install(app) {
        this.app = app;
        if (typeof window === "undefined") return;
        this.activity = new ActivityLog();
        this.activity.addEventListener("change", () => app.dispatchEvent(new CustomEvent("activity")));
        // Browsers keep audio suspended until a gesture; every tap may resume it.
        const unlock = () => this.sounds.unlock();
        window.addEventListener("pointerdown", unlock, { capture: true, passive: true });
        window.addEventListener("keydown", unlock, { capture: true });
        app.addEventListener("session", event => this.#attach(event.detail));
        app.addEventListener("online", event => this.#online(event.detail));
        app.addEventListener("call", () => this.#call());
        app.addEventListener("voice", () => this.#voice());
        app.addEventListener("groups", () => this.#groups());
        app.addEventListener("push", () => this.syncFilters());
        app.addEventListener("signedout", () => { this.away?.stop(); this.sounds.stopAll(); this.activity.clear(); });
        settings.onChange(({ changed }) => {
            if (changed.some(name => FILTER_SETTINGS.includes(name))) this.#scheduleFilters();
            if (changed.includes("autoAwayMinutes")) this.away?.activity();
        });
        for (const type of ["pointerdown", "keydown", "scroll"]) window.addEventListener(type, () => this.away?.activity(), { capture: true, passive: true });
        document.addEventListener("visibilitychange", () => {
            if (document.visibilityState === "visible") this.away?.activity();
        });
    }

    // --- Messages ------------------------------------------------------------------

    /**
     * Applies the notification policy to an incoming message and returns
     * the decision ({sound, toast, push, count}). info: {kind: "channel" |
     * "dm" | "group", mention, channelID, from, name, text, viewing}.
     */
    decide(tag, { kind, mention = false, channelID = null, from = "", name = "", text = "", viewing = false } = {}) {
        const all = settings.all();
        const event = kind === "channel" ? channelEvent({ mentioned: mention, text: text || "", keywords: all.keywords }) : kind;
        const decision = decide({ event, tag, channelID, from, viewing, settings: all });
        if (from && isBlocked(from)) return decision;
        const place = channelID !== null ? this.#channelName(channelID) : "";
        const title = kind === "channel" ? (place ? `${name} · ${place}` : name) : name || t("dm.someone");
        const preview = typeof text === "string" ? text.slice(0, 140) : "";
        if (!viewing && (event === "mention" || event === "keyword" || event === "dm" || event === "group")) {
            this.activity?.add({ kind: event === "mention" || event === "keyword" ? "mention" : "dm", title, body: preview, route: routeOf(tag), tag });
        }
        this.#alert(event, decision, { title, body: preview || t(`notify.event.${event}`), route: routeOf(tag) });
        return decision;
    }

    /** A conversation was read here or elsewhere. */
    read(tag) {
        this.activity?.markTagRead(tag);
    }

    #alert(event, decision, { title, body, route }) {
        if (decision.sound) {
            this.sounds.play(EVENT_SOUNDS[event] || "dm");
            if (["dm", "mention", "poke", "group"].includes(event)) navigator.vibrate?.(80);
        }
        if (decision.toast) this.banner(title, body, route);
    }

    /** An in-app banner at the top; tapping it opens route. */
    banner(title, body, route = "") {
        const host = document.getElementById("toasts");
        if (!host) return;
        const node = h(route ? "button" : "div", {
            class: "toast notify-banner", type: route ? "button" : null,
            onclick: route ? () => { node.remove(); location.hash = route; } : null,
        }, h("strong", {}, title), body ? h("span", {}, body) : null);
        host.append(node);
        setTimeout(() => node.remove(), 5000);
    }

    /** A system notification while the page is hidden but still running. */
    async #local(title, body, tag, route = "#/home") {
        if (document.visibilityState === "visible" || globalThis.Notification?.permission !== "granted") return;
        const registration = await navigator.serviceWorker?.getRegistration("/app/").catch(() => null);
        await registration?.showNotification(title, { body, tag, icon: "/app/icons/icon-192.png", data: { url: `/app/${route}`, tag } }).catch(() => {});
    }

    #channelName(id) {
        if (id === 0) return t("channels.global");
        const name = this.session?.channels().find(c => c.id === id)?.name;
        return name ? `#${name}` : "";
    }

    // --- Session ----------------------------------------------------------------------

    #attach(session) {
        this.session = session;
        this.watch = new PresenceWatch();
        setAvatarSource(uid => session.avatar(uid));
        session.addEventListener("poke", event => this.#poke(event.detail));
        session.addEventListener("whisper", event => this.#whisper(event.detail));
        session.addEventListener("kicked", event => this.#kicked(event.detail));
        session.addEventListener("snapshot", () => this.#presence());
        session.addEventListener("server-event", event => this.#serverEvent(event.detail));
        if (session.snapshot) this.#presence();
        this.#restoreStatus(session);
        this.away?.stop();
        this.away = new AutoAway({
            minutes: () => settings.get("autoAwayMinutes"),
            message: () => settings.get("autoAwayMessage") || "",
            current: () => ({ status: settings.get("presenceStatus") || "online", message: settings.get("presenceMessage") || "" }),
            apply: (status, message) => { if (this.session === session) session.setStatus(status, message).catch(() => {}); },
        });
    }

    /** Status lives per session: a reconnect starts online, so restore the choice. */
    #restoreStatus(session) {
        const status = settings.get("presenceStatus") || "online";
        const message = settings.get("presenceMessage") || "";
        if (status === "online" && !message) return;
        const apply = () => session.setStatus(status, message).catch(() => {});
        if (session.snapshot) apply();
        else session.addEventListener("snapshot", apply, { once: true });
    }

    /** Sets and remembers this phone's status. */
    async setStatus(status, message = "") {
        await this.session.setStatus(status, message);
        settings.set({ presenceStatus: status, presenceMessage: message });
        if (this.away) this.away.away = false;
    }

    #serverEvent({ type, data }) {
        const session = this.session;
        if (type === "avatar_changed" && data.unique_id) {
            session?.forgetAvatar(data.unique_id);
            refreshAvatars(data.unique_id);
        }
        if (type === "nickname_changed" && session?.snapshot) {
            const client = session.clients().find(c => c.client_id === data.client_id);
            if (client && data.nickname && client.nickname !== data.nickname) {
                client.nickname = data.nickname;
                session.dispatchEvent(new CustomEvent("snapshot", { detail: session.snapshot }));
            }
        }
    }

    #presence() {
        const session = this.session;
        if (!session?.snapshot) return;
        const all = settings.all();
        for (const change of this.watch.update(session, { buddies: all.buddies, channelWatch: all.channelWatch })) {
            const decision = decide({ event: change.type, settings: all });
            if (change.type === "buddy_online") {
                const body = t("notify.buddyOnline", { name: change.name });
                this.#alert("buddy_online", decision, { title: change.name, body, route: `#/dm/${encodeURIComponent(change.uid)}` });
                if (decision.sound) this.sounds.speak("buddy_online");
                if (decision.toast) this.#local(change.name, body, `buddy:${change.uid}`);
            } else {
                const body = t("notify.channelWatch", { channel: change.name, count: change.count });
                this.#alert("channel_watch", decision, { title: `#${change.name}`, body, route: "#/servers" });
                if (decision.sound) this.sounds.speak("channel_watch");
                if (decision.toast) this.#local(`#${change.name}`, body, `watch:${change.channelID}`, "#/servers");
            }
        }
    }

    #senderOf(clientID) {
        return this.session?.clients().find(c => c.client_id === clientID) || null;
    }

    #poke({ fromClientID, from, message, at }) {
        const sender = this.#senderOf(fromClientID);
        const uid = sender?.unique_id || "";
        if (uid && isBlocked(uid)) return;
        const decision = decide({ event: "poke", tag: uid ? `poke:${uid}` : "", from: uid, settings: settings.all() });
        this.activity?.add({ kind: "poke", title: from, body: message, route: uid ? `#/dm/${encodeURIComponent(uid)}` : "", tag: uid ? `poke:${uid}` : "" });
        if (decision.sound) {
            this.sounds.play("poke");
            this.sounds.speak("poke");
            navigator.vibrate?.([200, 100, 200]);
        }
        if (decision.toast) {
            import("./ui/poke-popup.js").then(({ showPoke }) => showPoke({ clientID: fromClientID, uid, name: from, message, at })).catch(() => {});
        }
    }

    #whispers = new Map();

    #whisper(data) {
        if (!data?.speaking || isBlocked(data.from_unique_id)) return;
        // One alert per whisperer and half minute, not one per sentence.
        const last = this.#whispers.get(data.from_client_id) || 0;
        if (Date.now() - last < 30000) return;
        this.#whispers.set(data.from_client_id, Date.now());
        const decision = decide({ event: "whisper", from: data.from_unique_id, settings: settings.all() });
        this.#alert("whisper", decision, { title: data.from_nickname || t("dm.someone"), body: t("notify.whisper") });
    }

    #kicked(data) {
        if (!this.session || data.client_id !== this.session.clientID) return;
        this.sounds.speak(data.ban ? "banned" : data.from_server ? "kicked" : "kicked_channel");
    }

    // --- App events --------------------------------------------------------------------

    #wasOnline = true;

    #online(online) {
        if (online === this.#wasOnline) return;
        this.#wasOnline = online;
        const key = online ? "connection_reconnected" : "connection_lost";
        this.sounds.play(key);
        this.sounds.speak(key);
    }

    #voiceChannel = 0;

    #voice() {
        const channel = this.app.voice?.active ? this.app.voice.channelID : 0;
        if (channel === this.#voiceChannel) return;
        const joined = !!channel;
        this.#voiceChannel = channel;
        this.sounds.speak(joined ? "channel_join" : "channel_leave");
    }

    #ringing = null;

    #call() {
        const calls = this.app.calls;
        const call = calls?.call;
        if (calls?.incoming && call) {
            if (isBlocked(call.caller)) {
                calls.decline().catch(() => {});
                return;
            }
            if (this.#ringing?.id === call.id) return;
            const name = this.session?.clients().find(c => c.unique_id === call.caller)?.nickname || t("dm.someone");
            this.#ringing = { id: call.id, name, route: `#/dm/${encodeURIComponent(call.caller)}`, tag: `call:${call.id}` };
            this.activity?.add({ kind: "call", title: name, body: t("notify.incomingCall"), route: this.#ringing.route, tag: this.#ringing.tag });
            if (decide({ event: "call", from: call.caller, settings: settings.all() }).sound) this.sounds.loop("ring");
            return;
        }
        if (this.#ringing) {
            const ringing = this.#ringing;
            this.#ringing = null;
            this.sounds.stopLoop("ring");
            // Ended before it was accepted here: a missed call stays unread.
            if (calls?.active && calls.call?.id === ringing.id) this.activity?.markTagRead(ringing.tag);
            else this.activity?.add({ kind: "call", title: ringing.name, body: t("notify.missedCall"), route: ringing.route, tag: ringing.tag });
        }
    }

    #invites = null;

    #groups() {
        const session = this.session;
        const groups = [...(this.app.groups?.list.values() || [])];
        const pending = groups.filter(g => g.members?.find(m => m.unique_id === session?.uniqueID)?.pending);
        const known = this.#invites;
        this.#invites = new Set(pending.map(g => g.id));
        if (!known) return;
        for (const group of pending.filter(g => !known.has(g.id))) {
            this.activity?.add({ kind: "invite", title: group.name, body: t("notify.invited"), route: "#/home", tag: `invite:${group.id}` });
            this.banner(group.name, t("notify.invited"), "#/home");
        }
    }

    // --- Push filters -----------------------------------------------------------------

    #filterTimer = 0;

    #scheduleFilters() {
        clearTimeout(this.#filterTimer);
        this.#filterTimer = setTimeout(() => this.syncFilters(), 400);
    }

    /** Sends the push filters when this phone receives pushes. */
    async syncFilters() {
        const app = this.app;
        if (!app?.session?.conn.open || !app.push?.preferences?.subscribed || app.meta.guest) return;
        try {
            app.push.preferences = await setPushPreferences(app.session.conn, { ...app.push.preferences, filters: pushFilters(settings.all()) });
        } catch {
            // The next change or connect tries again.
        }
    }
}

export const notifier = new Notifier();
