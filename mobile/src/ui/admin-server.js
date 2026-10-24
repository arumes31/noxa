// admin-server.js — server-wide administration: bans, complaints, the audit
// log, chat filters, custom emoji, and server settings (icon, banner,
// runtime configuration and media limits).
import {
    auditDetail, auditMatches, banStatus, durationParts, EMOJI_NAME, EMOJI_TYPES, emojiName, ICON_TYPES, joinList, splitList,
} from "../core/admin.js";
import { addStrings, lang, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import {
    adminScreen, busy, confirmSheet, emptyNote, fail, field, formSheet, input, loadingNote, nameOf, pickImage, select, textarea, textSheet, toggle, when,
} from "./admin-kit.js";

addStrings({
    en: {
        "bans.title": "Ban list",
        "bans.empty": "Nobody is banned.",
        "bans.by": "Banned by {name}",
        "bans.on": "on {date}",
        "bans.permanent": "Permanent",
        "bans.expires": "Expires {when}",
        "bans.expired": "Expired",
        "bans.noReason": "No reason given",
        "bans.lift": "Lift ban",
        "bans.liftAsk": "Lift the ban on {name}? They can sign in again.",
        "bans.lifted": "Ban lifted",
        "bans.type.0": "IP address",
        "bans.type.1": "Account",
        "bans.type.2": "Nickname",
        "complaints.title": "Complaints",
        "complaints.empty": "No complaints.",
        "complaints.count": "{count} complaints",
        "complaints.from": "From {name} · {date}",
        "complaints.clear": "Clear",
        "complaints.clearAll": "Clear all for {name}",
        "complaints.clearAsk": "Clear every complaint against {name}?",
        "complaints.cleared": "Complaints cleared",
        "audit.title": "Audit log",
        "audit.actor": "Who",
        "audit.action": "Action",
        "audit.target": "Target",
        "audit.from": "From",
        "audit.to": "Until",
        "audit.filter": "Filter",
        "audit.older": "Load older entries",
        "audit.empty": "No matching entries.",
        "audit.restricted": "Restricted entry",
        "audit.restrictedHint": "It concerns a channel you cannot see.",
        "audit.details": "Details",
        "audit.before": "Before",
        "audit.after": "After",
        "audit.channels": "Channels: {list}",
        "audit.revision": "Revision {revision}",
        "filters.title": "Chat filters",
        "filters.words": "Blocked words",
        "filters.wordsHint": "One per line. Messages containing any of them are refused (case-insensitive).",
        "filters.deny": "Blocked link hosts",
        "filters.denyHint": "One host per line, e.g. example.com (subdomains included).",
        "filters.allow": "Allowed link hosts",
        "filters.allowHint": "When set, links may only point to these hosts.",
        "filters.fromConfig": "These come from the server's configuration file until you save.",
        "emoji.title": "Custom emoji",
        "emoji.upload": "Upload emoji",
        "emoji.uploadHint": "PNG, GIF or WebP, up to 256 KiB.",
        "emoji.name": "Shortcode",
        "emoji.nameHint": "1–32 of a–z, 0–9, _ and -",
        "emoji.nameInvalid": "Use 1–32 lowercase letters, digits, _ or -.",
        "emoji.uploaded": ":{name}: added",
        "emoji.rename": "Rename",
        "emoji.renameTitle": "Rename :{name}:",
        "emoji.renamed": "Renamed to :{name}:",
        "emoji.delete": "Delete",
        "emoji.deleteAsk": "Delete :{name}:? Messages that used it show the shortcode instead.",
        "emoji.deleted": ":{name}: deleted",
        "emoji.empty": "No custom emoji yet.",
        "server.title": "Server settings",
        "server.icon": "Server icon",
        "server.banner": "Server banner",
        "server.change": "Choose image",
        "server.iconSaved": "Server icon saved",
        "server.bannerSaved": "Server banner saved",
        "server.none": "None set",
        "server.runtime": "Runtime settings",
        "server.maxClients": "Maximum connected members (0 = no limit)",
        "server.timeout": "Idle timeout, seconds (30–86400)",
        "server.bitrate": "Default Opus bitrate, bit/s (6000–510000)",
        "server.fec": "Forward error correction by default",
        "server.dtx": "Discontinuous transmission by default",
        "server.stereo": "Stereo by default",
        "server.saved": "Server settings saved",
        "server.media": "Media limits",
        "server.mediaHint": "Ceilings for camera and screen video. 0 means no limit.",
        "server.videoBitrate": "Video bitrate ceiling, kbit/s",
        "server.resolution": "Maximum resolution",
        "server.unlimited": "No limit",
        "server.mediaSaved": "Media limits saved",
        "server.mediaUnsupported": "This server manages media limits in its configuration file.",
    },
    de: {
        "bans.title": "Sperrliste",
        "bans.empty": "Niemand ist gesperrt.",
        "bans.by": "Gesperrt von {name}",
        "bans.on": "am {date}",
        "bans.permanent": "Dauerhaft",
        "bans.expires": "Endet {when}",
        "bans.expired": "Abgelaufen",
        "bans.noReason": "Kein Grund angegeben",
        "bans.lift": "Sperre aufheben",
        "bans.liftAsk": "Die Sperre von {name} aufheben? Die Person kann sich wieder anmelden.",
        "bans.lifted": "Sperre aufgehoben",
        "bans.type.0": "IP-Adresse",
        "bans.type.1": "Konto",
        "bans.type.2": "Spitzname",
        "complaints.title": "Beschwerden",
        "complaints.empty": "Keine Beschwerden.",
        "complaints.count": "{count} Beschwerden",
        "complaints.from": "Von {name} · {date}",
        "complaints.clear": "Erledigt",
        "complaints.clearAll": "Alle für {name} erledigen",
        "complaints.clearAsk": "Alle Beschwerden gegen {name} erledigen?",
        "complaints.cleared": "Beschwerden erledigt",
        "audit.title": "Prüfprotokoll",
        "audit.actor": "Wer",
        "audit.action": "Aktion",
        "audit.target": "Ziel",
        "audit.from": "Von",
        "audit.to": "Bis",
        "audit.filter": "Filtern",
        "audit.older": "Ältere Einträge laden",
        "audit.empty": "Keine passenden Einträge.",
        "audit.restricted": "Eingeschränkter Eintrag",
        "audit.restrictedHint": "Er betrifft einen Kanal, den du nicht sehen kannst.",
        "audit.details": "Details",
        "audit.before": "Vorher",
        "audit.after": "Nachher",
        "audit.channels": "Kanäle: {list}",
        "audit.revision": "Revision {revision}",
        "filters.title": "Chatfilter",
        "filters.words": "Gesperrte Wörter",
        "filters.wordsHint": "Eines pro Zeile. Nachrichten, die eines enthalten, werden abgelehnt (ohne Groß-/Kleinschreibung).",
        "filters.deny": "Gesperrte Link-Hosts",
        "filters.denyHint": "Ein Host pro Zeile, z. B. example.com (inklusive Subdomains).",
        "filters.allow": "Erlaubte Link-Hosts",
        "filters.allowHint": "Wenn gesetzt, dürfen Links nur auf diese Hosts zeigen.",
        "filters.fromConfig": "Diese Werte stammen aus der Konfigurationsdatei, bis du speicherst.",
        "emoji.title": "Eigene Emoji",
        "emoji.upload": "Emoji hochladen",
        "emoji.uploadHint": "PNG, GIF oder WebP, bis 256 KiB.",
        "emoji.name": "Kürzel",
        "emoji.nameHint": "1–32 Zeichen aus a–z, 0–9, _ und -",
        "emoji.nameInvalid": "Nutze 1–32 Kleinbuchstaben, Ziffern, _ oder -.",
        "emoji.uploaded": ":{name}: hinzugefügt",
        "emoji.rename": "Umbenennen",
        "emoji.renameTitle": ":{name}: umbenennen",
        "emoji.renamed": "Umbenannt in :{name}:",
        "emoji.delete": "Löschen",
        "emoji.deleteAsk": ":{name}: löschen? Nachrichten, die es nutzen, zeigen dann das Kürzel.",
        "emoji.deleted": ":{name}: gelöscht",
        "emoji.empty": "Noch keine eigenen Emoji.",
        "server.title": "Servereinstellungen",
        "server.icon": "Serversymbol",
        "server.banner": "Serverbanner",
        "server.change": "Bild wählen",
        "server.iconSaved": "Serversymbol gespeichert",
        "server.bannerSaved": "Serverbanner gespeichert",
        "server.none": "Nicht gesetzt",
        "server.runtime": "Laufzeiteinstellungen",
        "server.maxClients": "Höchstzahl verbundener Mitglieder (0 = unbegrenzt)",
        "server.timeout": "Leerlauf-Timeout, Sekunden (30–86400)",
        "server.bitrate": "Standard-Opus-Bitrate, bit/s (6000–510000)",
        "server.fec": "Vorwärtsfehlerkorrektur als Standard",
        "server.dtx": "Diskontinuierliche Übertragung als Standard",
        "server.stereo": "Stereo als Standard",
        "server.saved": "Servereinstellungen gespeichert",
        "server.media": "Medienlimits",
        "server.mediaHint": "Obergrenzen für Kamera- und Bildschirmvideo. 0 heißt unbegrenzt.",
        "server.videoBitrate": "Obergrenze Videobitrate, kbit/s",
        "server.resolution": "Höchste Auflösung",
        "server.unlimited": "Unbegrenzt",
        "server.mediaSaved": "Medienlimits gespeichert",
        "server.mediaUnsupported": "Dieser Server verwaltet Medienlimits in seiner Konfigurationsdatei.",
    },
});

const unavailable = (body, error) => body.replaceChildren(h("p", { class: "error" }, error.message), h("p", { class: "hint" }, t("admin.unavailable")));

/** "in 5 minutes" in the UI language. */
function relative(seconds) {
    const { value, unit } = durationParts(seconds);
    try { return new Intl.RelativeTimeFormat(lang(), { numeric: "auto" }).format(value, unit); } catch { return `${value} ${unit}`; }
}

// --- Bans -------------------------------------------------------------------------------

export function renderBans({ root, session, admin }) {
    const body = adminScreen(root, t("bans.title"), {}, loadingNote());
    const load = async () => {
        let bans;
        try { bans = await admin.bans(); } catch (error) { unavailable(body, error); return; }
        body.replaceChildren(bans.length ? h("ul", { class: "list admin-cards" }, bans.map(ban => {
            const status = banStatus(ban);
            const target = ban.type === 1 ? nameOf(session, ban.value) : ban.value;
            return h("li", { class: "card admin-ban" },
                h("p", { class: "row-title admin-wrap" }, target),
                h("p", { class: "row-sub" }, `${t(`bans.type.${ban.type}`)}${ban.type === 1 ? ` · ${ban.value}` : ""}`),
                h("p", { class: "admin-wrap" }, ban.reason || t("bans.noReason")),
                h("p", { class: "row-sub admin-wrap" }, [ban.banned_by ? t("bans.by", { name: nameOf(session, ban.banned_by) }) : "", t("bans.on", { date: when(ban.created_at) })].filter(Boolean).join(" ")),
                h("p", { class: `admin-pill${status.permanent ? " danger" : ""}` }, status.permanent ? t("bans.permanent") : status.expired ? t("bans.expired") : t("bans.expires", { when: relative(status.remaining) })),
                h("button", { class: "btn btn-block", type: "button", "aria-label": `${t("bans.lift")}: ${target}`, onclick: event => busy(event.currentTarget, async () => {
                    if (!await confirmSheet(t("bans.lift"), t("bans.liftAsk", { name: target }), t("bans.lift"))) return;
                    await admin.liftBan(ban.id);
                    toast(t("bans.lifted"));
                    await load();
                }) }, t("bans.lift")));
        })) : emptyNote(t("bans.empty")));
    };
    load();
}

// --- Complaints ---------------------------------------------------------------------

export function renderComplaints({ root, admin }) {
    const body = adminScreen(root, t("complaints.title"), {}, loadingNote());
    const render = entries => {
        const groups = new Map();
        for (const entry of entries) {
            if (!groups.has(entry.target_unique_id)) groups.set(entry.target_unique_id, []);
            groups.get(entry.target_unique_id).push(entry);
        }
        body.replaceChildren(groups.size ? h("ul", { class: "list admin-cards" }, [...groups].map(([target, list]) => {
            const name = list[0].target_nickname || target;
            return h("li", { class: "card" },
                h("p", { class: "row-title admin-wrap" }, name),
                h("p", { class: "row-sub" }, t("complaints.count", { count: list.length })),
                h("ul", { class: "list" }, list.map(entry => h("li", { class: "admin-complaint" },
                    h("div", { class: "row-main" }, h("p", { class: "admin-wrap" }, entry.reason),
                        h("p", { class: "row-sub admin-wrap" }, t("complaints.from", { name: entry.from_nickname || entry.from_unique_id, date: when(entry.created_at) }))),
                    h("button", { class: "btn", type: "button", onclick: event => busy(event.currentTarget, async () => {
                        render(await admin.clearComplaints(target, entry.from_unique_id));
                        toast(t("complaints.cleared"));
                    }) }, t("complaints.clear"))))),
                h("button", { class: "btn btn-danger btn-block", type: "button", onclick: event => busy(event.currentTarget, async () => {
                    if (!await confirmSheet(t("complaints.clearAll", { name }), t("complaints.clearAsk", { name }), t("complaints.clear"))) return;
                    render(await admin.clearComplaints(target));
                    toast(t("complaints.cleared"));
                }) }, t("complaints.clearAll", { name })));
        })) : emptyNote(t("complaints.empty")));
    };
    admin.complaints().then(render, error => unavailable(body, error));
}

// --- Audit log ----------------------------------------------------------------------------

function auditSheet(session, entry) {
    const detail = auditDetail(entry);
    const json = value => h("pre", { class: "admin-pre" }, JSON.stringify(value, null, 2));
    return formSheet(entry.restricted ? t("audit.restricted") : entry.action, h("div", { class: "admin-stack" },
        entry.restricted ? h("p", { class: "hint" }, t("audit.restrictedHint")) : h("dl", { class: "admin-dl" },
            h("dt", {}, t("audit.actor")), h("dd", {}, `${nameOf(session, entry.actor)} (${entry.actor})`),
            h("dt", {}, t("audit.target")), h("dd", {}, entry.target ? `${nameOf(session, entry.target)}${nameOf(session, entry.target) !== entry.target ? ` (${entry.target})` : ""}` : "—")),
        h("p", { class: "row-sub" }, when(entry.created_at)),
        detail.text ? h("p", { class: "admin-wrap" }, detail.text) : null,
        detail.channels.length ? h("p", { class: "row-sub" }, t("audit.channels", { list: detail.channels.map(id => session.channels().find(c => c.id === id)?.name || `#${id}`).join(", ") })) : null,
        detail.revision ? h("p", { class: "row-sub" }, t("audit.revision", { revision: detail.revision })) : null,
        detail.before !== undefined ? h("div", {}, h("p", { class: "section-label" }, t("audit.before")), json(detail.before)) : null,
        detail.after !== undefined ? h("div", {}, h("p", { class: "section-label" }, t("audit.after")), json(detail.after)) : null),
    [{ label: t("common.close"), value: null }]);
}

export function renderAudit({ root, session, admin }) {
    const body = adminScreen(root, t("audit.title"), {}, loadingNote());
    let entries = [], end = false;
    const filter = {};
    const list = h("ul", { class: "list" });
    const more = h("button", { class: "btn btn-block", type: "button", onclick: event => busy(event.currentTarget, () => page()) }, t("audit.older"));
    const names = () => new Map(session.clients().map(c => [c.unique_id, c.nickname]));
    const render = () => {
        const known = names();
        const shown = entries.filter(e => e.restricted ? !filter.actor && !filter.action && !filter.target && auditMatches(e, { from: filter.from, to: filter.to }) : auditMatches(e, filter, known));
        list.replaceChildren(...(shown.length ? shown.map(entry => h("li", {}, h("button", { class: "row admin-audit-row", type: "button", onclick: () => auditSheet(session, entry) },
            h("span", { class: "row-main" },
                h("span", { class: "row-title" }, entry.restricted ? t("audit.restricted") : entry.action),
                entry.restricted ? null : h("span", { class: "row-sub" }, [nameOf(session, entry.actor), entry.target ? `→ ${nameOf(session, entry.target)}` : ""].filter(Boolean).join(" "))),
            h("span", { class: "row-meta" }, when(entry.created_at))))) : [h("li", {}, emptyNote(t("audit.empty")))]));
        more.hidden = end;
    };
    const page = async () => {
        const result = await admin.audit(entries.at(-1)?.id || 0, 100);
        const next = result.entries || [];
        entries = [...entries, ...next];
        end = next.length < 100;
        render();
    };
    const text = (key, prop) => {
        const control = input({ type: "search", autocomplete: "off" });
        control.oninput = () => { filter[prop] = control.value.trim(); render(); };
        return field(t(key), control);
    };
    const date = (key, prop, endOfDay) => {
        const control = input({ type: "date" });
        control.onchange = () => {
            const value = control.value ? new Date(`${control.value}T00:00:00`).getTime() : 0;
            filter[prop] = value ? value + (endOfDay ? 86400000 : 0) : 0;
            render();
        };
        return field(t(key), control);
    };
    const filters = h("details", { class: "admin-details" }, h("summary", {}, t("audit.filter")),
        h("div", { class: "admin-stack" }, text("audit.actor", "actor"), text("audit.action", "action"), text("audit.target", "target"),
            h("div", { class: "admin-row-fields" }, date("audit.from", "from", false), date("audit.to", "to", true))));
    page().then(() => body.replaceChildren(filters, list, more), error => unavailable(body, error));
}

// --- Chat filters ---------------------------------------------------------------------------

export function renderFilters({ root, admin }) {
    const body = adminScreen(root, t("filters.title"), {}, loadingNote());
    const render = state => {
        const area = (key, label, hint) => {
            const control = textarea({ rows: "5", autocapitalize: "off", spellcheck: "false" });
            control.value = splitList(state[key]).join("\n");
            return { key, control, node: field(label, control, hint) };
        };
        const areas = [area("word_filter", t("filters.words"), t("filters.wordsHint")), area("link_blacklist", t("filters.deny"), t("filters.denyHint")), area("link_whitelist", t("filters.allow"), t("filters.allowHint"))];
        const submit = h("button", { class: "btn btn-primary btn-block", type: "submit" }, t("admin.save"));
        body.replaceChildren(h("form", { class: "admin-stack", onsubmit: event => {
            event.preventDefault();
            busy(submit, async () => {
                const patch = Object.fromEntries(areas.map(a => [a.key, joinList(splitList(a.control.value))]));
                render(await admin.setFilters(patch));
                toast(t("admin.saved"));
            });
        } }, state.from_config ? h("p", { class: "hint" }, t("filters.fromConfig")) : null, areas.map(a => a.node), h("div", { class: "admin-sticky" }, submit)));
    };
    admin.filters().then(render, error => unavailable(body, error));
}

// --- Custom emoji ----------------------------------------------------------------------------

export function renderEmoji({ root, session, admin }) {
    const body = adminScreen(root, t("emoji.title"), {}, loadingNote());
    const askName = (title, value, confirm) => textSheet(title, t("emoji.name"), { value, hint: t("emoji.nameHint"), confirm, required: true, maxLength: 32 });
    const valid = name => { if (EMOJI_NAME.test(name)) return true; toast(t("emoji.nameInvalid"), "warn"); return false; };
    const load = async () => {
        let emojis;
        try { emojis = await admin.emojiList(); } catch (error) { unavailable(body, error); return; }
        const upload = h("button", { class: "btn btn-primary btn-block", type: "button", onclick: event => busy(event.currentTarget, async () => {
            const image = await pickImage(EMOJI_TYPES);
            if (!image) return;
            const name = await askName(t("emoji.upload"), emojiName(image.name), t("emoji.upload"));
            if (!name || !valid(name)) return;
            await admin.emojiUpload(name, image.base64);
            toast(t("emoji.uploaded", { name }));
            await load();
        }) }, t("emoji.upload"));
        body.replaceChildren(upload, h("p", { class: "hint" }, t("emoji.uploadHint")),
            emojis.length ? h("ul", { class: "list admin-emoji" }, emojis.map(emoji => {
                const image = h("img", { class: "admin-emoji-img", alt: `:${emoji.name}:`, width: "32", height: "32", loading: "lazy" });
                session.emojiImage(emoji.name).then(url => { image.src = url; }).catch(() => {});
                return h("li", { class: "row" }, image,
                    h("span", { class: "row-main" }, h("span", { class: "row-title mono" }, `:${emoji.name}:`)),
                    h("button", { class: "btn", type: "button", onclick: event => busy(event.currentTarget, async () => {
                        const name = await askName(t("emoji.renameTitle", { name: emoji.name }), emoji.name, t("emoji.rename"));
                        if (!name || name === emoji.name || !valid(name)) return;
                        await admin.emojiRename(emoji.name, name);
                        toast(t("emoji.renamed", { name }));
                        await load();
                    }) }, t("emoji.rename")),
                    h("button", { class: "btn btn-danger", type: "button", "aria-label": `${t("emoji.delete")} :${emoji.name}:`, onclick: event => busy(event.currentTarget, async () => {
                        if (!await confirmSheet(t("emoji.delete"), t("emoji.deleteAsk", { name: emoji.name }), t("emoji.delete"))) return;
                        await admin.emojiDelete(emoji.name);
                        toast(t("emoji.deleted", { name: emoji.name }));
                        await load();
                    }) }, t("emoji.delete")));
            })) : emptyNote(t("emoji.empty")));
    };
    load();
}

// --- Server settings ------------------------------------------------------------------------

const RESOLUTIONS = [[0, 0], [640, 360], [1280, 720], [1920, 1080], [2560, 1440], [3840, 2160]];

function brandingCard(label, loadURL, save, savedKey, wide) {
    const image = h("img", { class: wide ? "admin-banner-preview" : "admin-icon-preview", alt: label, hidden: true });
    const none = h("p", { class: "hint" }, t("server.none"));
    const refresh = () => loadURL().then(url => { image.hidden = !url; none.hidden = !!url; if (url) image.src = url; }).catch(() => {});
    refresh();
    return h("section", { class: "card admin-stack", "aria-label": label }, h("h2", { class: "admin-card-title" }, label), image, none,
        h("button", { class: "btn", type: "button", onclick: event => busy(event.currentTarget, async () => {
            const picked = await pickImage(ICON_TYPES);
            if (!picked) return;
            await save(picked.base64);
            toast(t(savedKey));
            refresh();
        }) }, t("server.change")));
}

export function renderServerSettings({ root, session, admin }) {
    const body = adminScreen(root, t("server.title"), {}, loadingNote());
    (async () => {
        let config;
        try { config = await admin.serverConfig(); } catch (error) { unavailable(body, error); return; }
        const number = (label, key, min, max) => {
            const control = input({ type: "number", inputmode: "numeric", min: String(min), max: String(max), step: "1", required: true });
            control.value = String(config[key]);
            control.oninput = () => { config[key] = Math.trunc(Number(control.value) || 0); };
            return field(label, control);
        };
        const saveConfig = h("button", { class: "btn btn-primary btn-block", type: "submit" }, t("admin.save"));
        const runtime = h("form", { class: "card admin-stack", "aria-label": t("server.runtime"), onsubmit: event => {
            event.preventDefault();
            if (!runtime.reportValidity()) return;
            busy(saveConfig, async () => {
                const { max_clients, client_timeout_seconds, opus_bitrate, opus_fec, opus_dtx, opus_stereo } = config;
                Object.assign(config, await admin.setServerConfig({ max_clients, client_timeout_seconds, opus_bitrate, opus_fec, opus_dtx, opus_stereo }));
                toast(t("server.saved"));
            });
        } },
        h("h2", { class: "admin-card-title" }, t("server.runtime")),
        number(t("server.maxClients"), "max_clients", 0, 100000),
        number(t("server.timeout"), "client_timeout_seconds", 30, 86400),
        number(t("server.bitrate"), "opus_bitrate", 6000, 510000),
        toggle(t("server.fec"), config.opus_fec, v => { config.opus_fec = v; }),
        toggle(t("server.dtx"), config.opus_dtx, v => { config.opus_dtx = v; }),
        toggle(t("server.stereo"), config.opus_stereo, v => { config.opus_stereo = v; }),
        saveConfig);
        let media;
        if (config.media_limits_management) {
            const limits = session.mediaLimits || { video_max_bitrate: 0, video_max_width: 0, video_max_height: 0 };
            const bitrate = input({ type: "number", inputmode: "numeric", min: "0", max: "100000", step: "1" });
            bitrate.value = String(Math.round((limits.video_max_bitrate || 0) / 1000));
            const options = [...RESOLUTIONS];
            if (!options.some(([w, hgt]) => w === limits.video_max_width && hgt === limits.video_max_height)) options.push([limits.video_max_width, limits.video_max_height]);
            const resolution = select(options.map(([w, hgt]) => ({ value: `${w}x${hgt}`, label: w ? `${w} × ${hgt}` : t("server.unlimited") })), `${limits.video_max_width || 0}x${limits.video_max_height || 0}`);
            const saveMedia = h("button", { class: "btn btn-primary btn-block", type: "submit" }, t("admin.save"));
            media = h("form", { class: "card admin-stack", "aria-label": t("server.media"), onsubmit: event => {
                event.preventDefault();
                busy(saveMedia, async () => {
                    const [width, height] = resolution.value.split("x").map(Number);
                    await admin.setMediaLimits({ video_max_bitrate: Math.max(0, Math.round(Number(bitrate.value) || 0)) * 1000, video_max_width: width, video_max_height: height });
                    toast(t("server.mediaSaved"));
                });
            } }, h("h2", { class: "admin-card-title" }, t("server.media")), h("p", { class: "hint" }, t("server.mediaHint")),
            field(t("server.videoBitrate"), bitrate), field(t("server.resolution"), resolution), saveMedia);
        } else {
            media = h("section", { class: "card" }, h("h2", { class: "admin-card-title" }, t("server.media")), h("p", { class: "hint" }, t("server.mediaUnsupported")));
        }
        body.replaceChildren(
            brandingCard(t("server.icon"), () => admin.serverIconURL(), data => admin.setServerIcon(data), "server.iconSaved", false),
            brandingCard(t("server.banner"), () => admin.serverBannerURL(), data => admin.setServerBanner(data), "server.bannerSaved", true),
            runtime, media);
    })().catch(fail);
}
