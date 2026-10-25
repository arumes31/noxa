// admin-channels.js — channel administration: the per-channel hub, create
// and edit forms, move and delete, icons, access rules with impact preview
// and access checks, invitation links and webhooks.
import { app } from "../app.js";
import {
    applyPreset, capabilityLabel, effectOf, groupCapabilities, MAIN_CAPABILITIES, presetOf, PRESETS, roleAllowed, setSynced, setWithDependencies,
} from "../core/access-matrix.js";
import { ICON_TYPES, inviteLink } from "../core/admin.js";
import { addStrings, lang, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import {
    adminScreen, busy, chooseSheet, confirmSheet, copyBox, emptyNote, field, input, linkRow, loadingNote, pickImage, select, textarea, textSheet, toggle, when,
} from "./admin-kit.js";

addStrings({
    en: {
        "chan.settings": "Channel settings",
        "chan.menu": "Settings of {name}",
        "chan.edit": "Edit channel",
        "chan.editSub": "Name, topic, limits, voice quality, password",
        "chan.access": "Access and permissions",
        "chan.accessSub": "Who can see, write and talk",
        "chan.invite": "Invite link",
        "chan.inviteSub": "Share a link to this channel",
        "chan.webhooks": "Webhooks",
        "chan.webhooksSub": "Let other services post here",
        "chan.icon": "Channel icon",
        "chan.iconUpload": "Upload image",
        "chan.iconCopy": "Copy from another channel",
        "chan.iconCopyTitle": "Copy the icon of",
        "chan.iconSaved": "Channel icon saved",
        "chan.noIcon": "No icon yet",
        "chan.move": "Move channel…",
        "chan.moveTitle": "Move {name} into",
        "chan.root": "Top level",
        "chan.moveAccess": "Access after moving",
        "chan.keep": "Keep its current access",
        "chan.sync": "Use the new parent's access",
        "chan.moved": "Channel moved",
        "chan.noDestinations": "There is nowhere you may move this channel.",
        "chan.addSub": "Add channel inside",
        "chan.delete": "Delete channel…",
        "chan.deleteAsk": "Delete “{name}”? {count} channels including sub-channels are removed with their messages.",
        "chan.deleted": "Channel deleted",
        "chan.new": "New channel",
        "chan.parent": "Inside",
        "chan.name": "Name",
        "chan.topic": "Topic",
        "chan.description": "Description",
        "chan.textOnly": "Text channel (chat only, no voice)",
        "chan.textUnavailable": "Text channels need permission to create permanent channels.",
        "chan.lifetime": "Lifetime",
        "chan.temporary": "Temporary (removed when empty)",
        "chan.semiPermanent": "Until the server restarts",
        "chan.permanent": "Permanent",
        "chan.preset": "Who can use it",
        "chan.preset.inherit": "Same as the parent",
        "chan.privateRoles": "Roles that can see it",
        "chan.password": "Voice password (optional)",
        "chan.passwordAction": "Voice password",
        "chan.passwordKeep": "Keep",
        "chan.passwordSet": "Set a new password",
        "chan.passwordRemove": "Remove the password",
        "chan.newPassword": "New password",
        "chan.textNoPassword": "Text channels have no password.",
        "chan.advanced": "Limits and voice quality",
        "chan.maxClients": "Maximum members in voice (0 = no limit)",
        "chan.slowMode": "Slow mode, seconds between messages",
        "chan.order": "Order (lower comes first)",
        "chan.bitrate": "Opus bitrate (bit/s, 0 = server default)",
        "chan.fec": "Forward error correction",
        "chan.dtx": "Discontinuous transmission (save data in silence)",
        "chan.stereo": "Stereo",
        "chan.create": "Create channel",
        "chan.created": "Channel created",
        "chan.saved": "Channel saved",
        "chan.pending": "Saved; the server is still applying it.",
        "chan.nameRequired": "Enter a name.",
        "acc.title": "Access",
        "acc.synced": "Same as the parent channel",
        "acc.syncedHint": "Turn off to set rules for this channel only.",
        "acc.noParent": "Follows the server-wide role permissions. Turn off to set rules for this channel.",
        "acc.parentHidden": "You cannot manage the parent channel's access.",
        "acc.presets": "Presets for everyone",
        "acc.preset.public": "Public",
        "acc.preset.private": "Private",
        "acc.preset.readOnly": "Read-only",
        "acc.preset.listenOnly": "Listen-only",
        "acc.subject": "Rules for",
        "acc.member": "Member: {name}",
        "acc.memberRef": "Member #{id}",
        "acc.addMember": "Add a member…",
        "acc.inherit": "Inherit",
        "acc.allow": "Allow",
        "acc.deny": "Deny",
        "acc.now": "Now: {state}",
        "acc.allowed": "allowed",
        "acc.denied": "denied",
        "acc.all": "All channel permissions",
        "acc.locked": "You cannot grant this permission.",
        "acc.preview": "Preview changes",
        "acc.previewFirst": "Preview the changes before saving.",
        "acc.unchanged": "Nothing changes for this role.",
        "acc.guests": "Guests",
        "acc.impact": "What changes",
        "acc.change": "{capability}: {before} → {after}",
        "acc.save": "Save access",
        "acc.discard": "Discard changes",
        "acc.savedNote": "Access saved",
        "acc.unsaved": "Unsaved changes",
        "acc.check": "Check access for a member",
        "acc.checkWho": "Member",
        "acc.checkSearch": "Find member",
        "acc.checkWhat": "Permission",
        "acc.checkButton": "Check",
        "acc.checkSaved": "Checks use the saved rules, not unsaved changes.",
        "acc.found": "{count} found",
        "acc.result": "{state} — {reason}",
        "acc.roles": "Roles: {roles}",
        "acc.manageable": "You can moderate this member.",
        "acc.notManageable": "This member ranks at or above you.",
        "acc.reason.owner": "server owner",
        "acc.reason.administrator": "administrator role",
        "acc.reason.role_grant": "granted by a role",
        "acc.reason.everyone_override": "channel rule for everyone",
        "acc.reason.role_override": "channel rule for a role",
        "acc.reason.member_override": "channel rule for this member",
        "acc.reason.requires": "needs another permission first",
        "acc.reason.not_granted": "no role grants it",
        "acc.reason.home_channel": "home channel rules",
        "acc.reason.unknown_channel": "unknown channel",
        "acc.reason.invalid_scope": "not a channel permission",
        "acc.reason.invalid_context": "not applicable",
        "inv.title": "Invite link",
        "inv.help": "Anyone with the link finds this channel. It never bypasses the channel's permissions or password.",
        "inv.active": "A link is active ({access}). For security it can only be shown when it is created.",
        "inv.none": "No invite link yet.",
        "inv.access": "Who may use it",
        "inv.guest": "Guests and members",
        "inv.member": "Registered members only",
        "inv.create": "Create link",
        "inv.regenerate": "Make a new link",
        "inv.regenerateAsk": "The current link stops working.",
        "inv.revoke": "Revoke link",
        "inv.revoked": "Invite link revoked",
        "inv.ready": "Link ready — copy or share it now.",
        "inv.link": "Invite link",
        "inv.shareTitle": "Join {name}",
        "hook.title": "Webhooks",
        "hook.help": "A webhook lets another service post messages to this channel with a secret token.",
        "hook.name": "Name",
        "hook.create": "Create webhook",
        "hook.revoke": "Revoke",
        "hook.revokeAsk": "Revoke the webhook “{name}”? Services using it can no longer post.",
        "hook.revoked": "Webhook revoked",
        "hook.created": "Created {date}",
        "hook.secret": "Copy the token now. It is shown only once.",
        "hook.endpoint": "Endpoint (POST, JSON {\"content\": \"…\"})",
        "hook.token": "Bearer token",
        "hook.port": "The endpoint is served on the server's health port {port}.",
        "hook.empty": "No webhooks yet.",
    },
    de: {
        "chan.settings": "Kanaleinstellungen",
        "chan.menu": "Einstellungen von {name}",
        "chan.edit": "Kanal bearbeiten",
        "chan.editSub": "Name, Thema, Limits, Sprachqualität, Passwort",
        "chan.access": "Zugriff und Berechtigungen",
        "chan.accessSub": "Wer sehen, schreiben und sprechen darf",
        "chan.invite": "Einladungslink",
        "chan.inviteSub": "Einen Link zu diesem Kanal teilen",
        "chan.webhooks": "Webhooks",
        "chan.webhooksSub": "Andere Dienste hier posten lassen",
        "chan.icon": "Kanalsymbol",
        "chan.iconUpload": "Bild hochladen",
        "chan.iconCopy": "Von anderem Kanal kopieren",
        "chan.iconCopyTitle": "Symbol kopieren von",
        "chan.iconSaved": "Kanalsymbol gespeichert",
        "chan.noIcon": "Noch kein Symbol",
        "chan.move": "Kanal verschieben…",
        "chan.moveTitle": "{name} verschieben nach",
        "chan.root": "Oberste Ebene",
        "chan.moveAccess": "Zugriff nach dem Verschieben",
        "chan.keep": "Bisherigen Zugriff behalten",
        "chan.sync": "Zugriff des neuen Oberkanals übernehmen",
        "chan.moved": "Kanal verschoben",
        "chan.noDestinations": "Du darfst diesen Kanal nirgendwohin verschieben.",
        "chan.addSub": "Kanal darin hinzufügen",
        "chan.delete": "Kanal löschen…",
        "chan.deleteAsk": "„{name}“ löschen? {count} Kanäle einschließlich Unterkanälen werden mit ihren Nachrichten entfernt.",
        "chan.deleted": "Kanal gelöscht",
        "chan.new": "Neuer Kanal",
        "chan.parent": "In",
        "chan.name": "Name",
        "chan.topic": "Thema",
        "chan.description": "Beschreibung",
        "chan.textOnly": "Textkanal (nur Chat, keine Sprache)",
        "chan.textUnavailable": "Textkanäle erfordern die Berechtigung, dauerhafte Kanäle zu erstellen.",
        "chan.lifetime": "Lebensdauer",
        "chan.temporary": "Temporär (verschwindet, wenn leer)",
        "chan.semiPermanent": "Bis zum Serverneustart",
        "chan.permanent": "Dauerhaft",
        "chan.preset": "Wer ihn nutzen darf",
        "chan.preset.inherit": "Wie der Oberkanal",
        "chan.privateRoles": "Rollen, die ihn sehen",
        "chan.password": "Sprachpasswort (optional)",
        "chan.passwordAction": "Sprachpasswort",
        "chan.passwordKeep": "Beibehalten",
        "chan.passwordSet": "Neues Passwort setzen",
        "chan.passwordRemove": "Passwort entfernen",
        "chan.newPassword": "Neues Passwort",
        "chan.textNoPassword": "Textkanäle haben kein Passwort.",
        "chan.advanced": "Limits und Sprachqualität",
        "chan.maxClients": "Höchstzahl im Sprachkanal (0 = unbegrenzt)",
        "chan.slowMode": "Langsamer Modus, Sekunden zwischen Nachrichten",
        "chan.order": "Reihenfolge (kleiner zuerst)",
        "chan.bitrate": "Opus-Bitrate (bit/s, 0 = Servervorgabe)",
        "chan.fec": "Vorwärtsfehlerkorrektur",
        "chan.dtx": "Diskontinuierliche Übertragung (spart Daten in Stille)",
        "chan.stereo": "Stereo",
        "chan.create": "Kanal erstellen",
        "chan.created": "Kanal erstellt",
        "chan.saved": "Kanal gespeichert",
        "chan.pending": "Gespeichert; der Server wendet es noch an.",
        "chan.nameRequired": "Gib einen Namen ein.",
        "acc.title": "Zugriff",
        "acc.synced": "Wie der Oberkanal",
        "acc.syncedHint": "Ausschalten, um Regeln nur für diesen Kanal festzulegen.",
        "acc.noParent": "Folgt den serverweiten Rollenberechtigungen. Ausschalten, um Regeln für diesen Kanal festzulegen.",
        "acc.parentHidden": "Du kannst den Zugriff des Oberkanals nicht verwalten.",
        "acc.presets": "Vorlagen für alle",
        "acc.preset.public": "Öffentlich",
        "acc.preset.private": "Privat",
        "acc.preset.readOnly": "Nur lesen",
        "acc.preset.listenOnly": "Nur zuhören",
        "acc.subject": "Regeln für",
        "acc.member": "Mitglied: {name}",
        "acc.memberRef": "Mitglied #{id}",
        "acc.addMember": "Mitglied hinzufügen…",
        "acc.inherit": "Erben",
        "acc.allow": "Erlauben",
        "acc.deny": "Verweigern",
        "acc.now": "Derzeit: {state}",
        "acc.allowed": "erlaubt",
        "acc.denied": "verweigert",
        "acc.all": "Alle Kanalberechtigungen",
        "acc.locked": "Diese Berechtigung kannst du nicht vergeben.",
        "acc.preview": "Änderungen prüfen",
        "acc.previewFirst": "Prüfe die Änderungen vor dem Speichern.",
        "acc.unchanged": "Für diese Rolle ändert sich nichts.",
        "acc.guests": "Gäste",
        "acc.impact": "Was sich ändert",
        "acc.change": "{capability}: {before} → {after}",
        "acc.save": "Zugriff speichern",
        "acc.discard": "Änderungen verwerfen",
        "acc.savedNote": "Zugriff gespeichert",
        "acc.unsaved": "Ungespeicherte Änderungen",
        "acc.check": "Zugriff eines Mitglieds prüfen",
        "acc.checkWho": "Mitglied",
        "acc.checkSearch": "Mitglied suchen",
        "acc.checkWhat": "Berechtigung",
        "acc.checkButton": "Prüfen",
        "acc.checkSaved": "Die Prüfung nutzt die gespeicherten Regeln, nicht ungespeicherte Änderungen.",
        "acc.found": "{count} gefunden",
        "acc.result": "{state} – {reason}",
        "acc.roles": "Rollen: {roles}",
        "acc.manageable": "Du kannst dieses Mitglied moderieren.",
        "acc.notManageable": "Dieses Mitglied steht gleich oder über dir.",
        "acc.reason.owner": "Servereigentümer",
        "acc.reason.administrator": "Administratorrolle",
        "acc.reason.role_grant": "durch eine Rolle gewährt",
        "acc.reason.everyone_override": "Kanalregel für alle",
        "acc.reason.role_override": "Kanalregel für eine Rolle",
        "acc.reason.member_override": "Kanalregel für dieses Mitglied",
        "acc.reason.requires": "braucht zuerst eine andere Berechtigung",
        "acc.reason.not_granted": "keine Rolle gewährt sie",
        "acc.reason.home_channel": "Regeln des Startkanals",
        "acc.reason.unknown_channel": "unbekannter Kanal",
        "acc.reason.invalid_scope": "keine Kanalberechtigung",
        "acc.reason.invalid_context": "nicht anwendbar",
        "inv.title": "Einladungslink",
        "inv.help": "Wer den Link hat, findet diesen Kanal. Er umgeht nie die Berechtigungen oder das Passwort des Kanals.",
        "inv.active": "Ein Link ist aktiv ({access}). Aus Sicherheitsgründen wird er nur beim Erstellen angezeigt.",
        "inv.none": "Noch kein Einladungslink.",
        "inv.access": "Wer ihn nutzen darf",
        "inv.guest": "Gäste und Mitglieder",
        "inv.member": "Nur registrierte Mitglieder",
        "inv.create": "Link erstellen",
        "inv.regenerate": "Neuen Link erstellen",
        "inv.regenerateAsk": "Der bisherige Link funktioniert dann nicht mehr.",
        "inv.revoke": "Link widerrufen",
        "inv.revoked": "Einladungslink widerrufen",
        "inv.ready": "Link bereit – jetzt kopieren oder teilen.",
        "inv.link": "Einladungslink",
        "inv.shareTitle": "{name} beitreten",
        "hook.title": "Webhooks",
        "hook.help": "Ein Webhook lässt einen anderen Dienst mit einem geheimen Token Nachrichten in diesen Kanal schreiben.",
        "hook.name": "Name",
        "hook.create": "Webhook erstellen",
        "hook.revoke": "Widerrufen",
        "hook.revokeAsk": "Den Webhook „{name}“ widerrufen? Dienste, die ihn nutzen, können nicht mehr posten.",
        "hook.revoked": "Webhook widerrufen",
        "hook.created": "Erstellt {date}",
        "hook.secret": "Kopiere das Token jetzt. Es wird nur einmal angezeigt.",
        "hook.endpoint": "Endpunkt (POST, JSON {\"content\": \"…\"})",
        "hook.token": "Bearer-Token",
        "hook.port": "Der Endpunkt läuft auf dem Health-Port {port} des Servers.",
        "hook.empty": "Noch keine Webhooks.",
    },
});

const channelOf = (session, id) => session.channels().find(c => c.id === id);
const unavailable = (body, error) => body.replaceChildren(h("p", { class: "error" }, error.message), h("p", { class: "hint" }, t("admin.unavailable")));

// --- Channel hub ----------------------------------------------------------------------

async function iconSheet(admin, session, channel, preview) {
    const others = session.channels().filter(c => c.id !== channel.id && !c.home);
    const choice = await chooseSheet(t("chan.icon"), [
        { value: "upload", label: t("chan.iconUpload") },
        { value: "copy", label: t("chan.iconCopy"), disabled: !others.length },
    ]);
    if (choice === "upload") {
        const image = await pickImage(ICON_TYPES);
        if (!image) return;
        await admin.channelIcon(channel.id, { data: image.base64 });
    } else if (choice === "copy") {
        const from = await chooseSheet(t("chan.iconCopyTitle"), others.map(c => ({ value: c.id, label: c.name })));
        if (from === null) return;
        await admin.channelIcon(channel.id, { copyFrom: from });
    } else return;
    toast(t("chan.iconSaved"));
    preview();
}

async function moveSheet(admin, channel) {
    const state = await admin.channelState("channel_move", channel.id);
    if (!state.destinations?.length) { toast(t("chan.noDestinations"), "warn"); return; }
    const parent = await chooseSheet(t("chan.moveTitle", { name: channel.name }),
        state.destinations.map(d => ({ value: d.id, label: d.id ? d.name : t("chan.root"), disabled: d.id === channel.parent })));
    if (parent === null) return;
    const destination = state.destinations.find(d => d.id === parent);
    const sync = destination?.can_sync ? await chooseSheet(t("chan.moveAccess"), [{ value: false, label: t("chan.keep") }, { value: true, label: t("chan.sync") }]) : false;
    if (sync === null) return;
    const result = await admin.channelChange({ kind: "channel_move", expected_revision: state.revision, channel_id: channel.id, parent_id: parent, sync_to_parent: !!sync });
    toast(t(result.enforcement_pending ? "chan.pending" : "chan.moved"));
}

export function renderChannelAdmin({ root, session, admin }, id) {
    const channel = channelOf(session, id);
    if (!channel) { location.hash = "#/admin"; return; }
    const image = h("img", { class: "admin-icon-preview", alt: "", hidden: true });
    const note = h("span", { class: "row-sub" }, t("chan.noIcon"));
    const preview = () => admin.channelIconURL(id).then(url => {
        image.hidden = !url;
        note.hidden = !!url;
        if (url) image.src = url;
    }).catch(() => {});
    const button = (label, run, { danger = false, iconName } = {}) => h("li", {}, h("button", { class: `row admin-link${danger ? " danger" : ""}`, type: "button", onclick: event => busy(event.currentTarget, run) },
        iconName ? icon(iconName) : null, h("span", { class: "row-main" }, h("span", { class: "row-title" }, label))));
    adminScreen(root, channel.name, { sub: t("chan.settings"), back: channel.chat ? `#/chat/${id}` : "#/admin" },
        h("div", { class: "card row admin-icon-card" }, image, note,
            h("button", { class: "btn", type: "button", onclick: event => busy(event.currentTarget, () => iconSheet(admin, session, channel, preview)) }, t("chan.icon"))),
        h("ul", { class: "list" },
            linkRow(`#/admin/channel-edit/${id}`, t("chan.edit"), t("chan.editSub")),
            linkRow(`#/admin/access/${id}`, t("chan.access"), t("chan.accessSub"), { iconNode: icon("lock") }),
            linkRow(`#/admin/invites/${id}`, t("chan.invite"), t("chan.inviteSub"), { iconNode: icon("link") }),
            channel.chat ? linkRow(`#/admin/webhooks/${id}`, t("chan.webhooks"), t("chan.webhooksSub")) : null,
            linkRow(`#/admin/channel-new/${id}`, t("chan.addSub"), "", { iconNode: icon("plus") }),
            button(t("chan.move"), () => moveSheet(admin, channel)),
            button(t("chan.delete"), async () => {
                const state = await admin.channelState("channel_delete", id);
                if (!await confirmSheet(t("chan.delete").replace("…", ""), t("chan.deleteAsk", { name: channel.name, count: state.affected_channels || 1 }), t("admin.delete"))) return;
                const result = await admin.channelChange({ kind: "channel_delete", expected_revision: state.revision, channel_id: id });
                toast(t(result.enforcement_pending ? "chan.pending" : "chan.deleted"));
                location.hash = "#/admin";
            }, { danger: true })));
    preview();
}

// --- Create and edit ---------------------------------------------------------------

const PRESET_NAMES = ["inherit", ...Object.keys(PRESETS)];

export function renderChannelForm({ root, session, admin }, kind, id) {
    const creating = kind === "channel_create";
    const channel = channelOf(session, id);
    if (!creating && !channel) { location.hash = "#/admin"; return; }
    const body = adminScreen(root, creating ? t("chan.new") : channel.name, { back: creating ? (id ? `#/admin/channel/${id}` : "#/admin") : `#/admin/channel/${id}`, sub: creating ? null : t("chan.edit") }, loadingNote());
    (async () => {
        let state;
        try { state = await admin.channelState(kind, id); } catch (error) { unavailable(body, error); return; }
        const settings = structuredClone(state.settings);
        let textOnly = false, channelType = state.can_create_permanent ? 2 : 0, preset = "inherit", passwordAction = "keep";
        const privateRoles = new Set();
        const text = (key, label, opts = {}) => {
            const control = opts.multiline ? textarea({ maxlength: "1024" }) : input({ maxlength: opts.max || "1024", required: !!opts.required, autocomplete: "off" });
            control.value = settings[key] ?? "";
            control.oninput = () => { settings[key] = control.value; };
            return field(label, control);
        };
        const number = (key, label, min, max) => {
            const control = input({ type: "number", inputmode: "numeric", min: String(min), max: String(max), step: "1" });
            control.value = String(settings[key] ?? 0);
            control.oninput = () => { settings[key] = Math.trunc(Number(control.value) || 0); };
            return field(label, control);
        };
        const passwordBox = input({ type: "password", maxlength: "4096", autocomplete: "new-password" });
        const parentName = id ? channelOf(session, id)?.name || `#${id}` : t("chan.root");
        const form = h("form", { class: "admin-stack" });
        const render = () => {
            const editingText = !creating && channel.textOnly;
            const presetSelect = select(PRESET_NAMES.map(key => {
                const required = key === "inherit" ? [] : applyPreset([], state.everyone_id, key).map(o => o.capability);
                return { value: key, label: key === "inherit" ? t("chan.preset.inherit") : t(`acc.preset.${key}`),
                    disabled: key !== "inherit" && (!state.can_manage_access || required.some(c => !(state.grantable_capabilities || []).includes(c))) };
            }), preset);
            presetSelect.onchange = () => { preset = presetSelect.value; render(); };
            const lifetime = select([
                { value: 0, label: t("chan.temporary"), disabled: !state.can_create_temporary || textOnly },
                { value: 1, label: t("chan.semiPermanent"), disabled: !state.can_create_permanent },
                { value: 2, label: t("chan.permanent"), disabled: !state.can_create_permanent },
            ], channelType);
            lifetime.onchange = () => { channelType = Number(lifetime.value); };
            const passwordMode = select([
                { value: "keep", label: t("chan.passwordKeep") },
                { value: "set", label: t("chan.passwordSet"), disabled: editingText },
                { value: "remove", label: t("chan.passwordRemove") },
            ], passwordAction);
            passwordMode.onchange = () => { passwordAction = passwordMode.value; passwordBox.value = ""; render(); };
            const advanced = h("details", { class: "admin-details", open: form.querySelector("details")?.open },
                h("summary", {}, t("chan.advanced")),
                h("div", { class: "admin-stack" },
                    number("max_clients", t("chan.maxClients"), 0, 2147483647),
                    number("slow_mode_seconds", t("chan.slowMode"), 0, 2147483647),
                    number("order_index", t("chan.order"), -2147483648, 2147483647),
                    number("opus_bitrate", t("chan.bitrate"), 0, 510000),
                    toggle(t("chan.fec"), settings.opus_fec, v => { settings.opus_fec = v; }),
                    toggle(t("chan.dtx"), settings.opus_dtx, v => { settings.opus_dtx = v; }),
                    toggle(t("chan.stereo"), settings.opus_stereo, v => { settings.opus_stereo = v; })));
            const submit = h("button", { class: "btn btn-primary btn-block", type: "submit" }, creating ? t("chan.create") : t("admin.save"));
            form.onsubmit = event => {
                event.preventDefault();
                if (!settings.name?.trim()) { toast(t("chan.nameRequired"), "warn"); form.querySelector("input")?.focus(); return; }
                busy(submit, () => save());
            };
            form.replaceChildren(
                creating ? h("p", { class: "hint" }, `${t("chan.parent")}: ${parentName}`) : null,
                text("name", t("chan.name"), { required: true, max: "255" }),
                text("topic", t("chan.topic")),
                text("description", t("chan.description"), { multiline: true }),
                creating ? toggle(t("chan.textOnly"), textOnly, v => { textOnly = v; if (v) channelType = 2; render(); },
                    { disabled: !state.supports_text_channels || !state.can_create_permanent, hint: state.can_create_permanent ? null : t("chan.textUnavailable") }) : null,
                creating ? field(t("chan.lifetime"), lifetime) : null,
                creating && state.can_manage_access ? field(t("chan.preset"), presetSelect) : null,
                creating && preset === "private" ? h("fieldset", { class: "admin-fieldset" }, h("legend", { class: "section-label" }, t("chan.privateRoles")),
                    (state.roles || []).map(role => toggle(role.name, privateRoles.has(role.id), v => { v ? privateRoles.add(role.id) : privateRoles.delete(role.id); }))) : null,
                creating && !textOnly ? field(t("chan.password"), passwordBox) : null,
                !creating ? field(t("chan.passwordAction"), passwordMode, editingText ? t("chan.textNoPassword") : null) : null,
                !creating && passwordAction === "set" ? field(t("chan.newPassword"), passwordBox) : null,
                advanced,
                h("div", { class: "admin-sticky" }, submit));
        };
        const save = async () => {
            const request = { kind, expected_revision: state.revision, channel_id: creating ? 0 : id, settings: { ...settings, name: settings.name.trim() } };
            if (creating) {
                request.parent_id = id;
                request.channel_type = channelType;
                if (textOnly) request.text_only = true; else if (passwordBox.value) request.password = passwordBox.value;
                if (preset !== "inherit") {
                    const overrides = applyPreset([], state.everyone_id, preset);
                    if (preset === "private") for (const roleID of privateRoles) overrides.push({ role_id: roleID, capability: "view_channel", effect: "allow" });
                    request.access = { synced: false, overrides };
                }
            } else if (passwordAction !== "keep") {
                request.set_password = true;
                request.password = passwordAction === "set" ? passwordBox.value : "";
            }
            const result = await admin.channelChange(request);
            toast(t(result.enforcement_pending ? "chan.pending" : creating ? "chan.created" : "chan.saved"));
            location.hash = `#/admin/channel/${creating ? result.channel_id : id}`;
        };
        render();
        body.replaceChildren(form);
        form.querySelector("input")?.focus();
    })();
}

// --- Access ------------------------------------------------------------------------------

function effectControl(label, value, onchange, { disabled, hint } = {}) {
    const group = h("div", { class: "admin-segments", role: "radiogroup", "aria-label": label });
    for (const effect of ["inherit", "allow", "deny"]) {
        group.append(h("button", { type: "button", class: `btn admin-segment effect-${effect}`, role: "radio", "aria-checked": String(value === effect), disabled: !!disabled,
            onclick: () => { if (value !== effect) onchange(effect); } }, t(`acc.${effect}`)));
    }
    return h("div", { class: "admin-effect" }, h("span", { class: "row-main" }, h("span", { class: "row-title admin-wrap" }, label), hint ? h("span", { class: "row-sub" }, hint) : null), group);
}

export function renderAccess({ root, session, admin }, id) {
    const channel = channelOf(session, id);
    if (!channel) { location.hash = "#/admin"; return; }
    const body = adminScreen(root, channel.name, { sub: t("chan.access"), back: `#/admin/channel/${id}` }, loadingNote());
    let state, saved, draft, subject, reviewed = "", openAll = false;
    const names = new Map();
    const status = h("p", { class: "hint", role: "status" });
    const impact = h("div", { class: "admin-impact", "aria-live": "polite" });
    const load = async () => {
        try { state = await admin.roleState(id); } catch (error) { unavailable(body, error); return; }
        const policy = state.policy.channels.find(c => c.channel_id === id);
        if (!policy) { unavailable(body, new Error(t("admin.unavailable"))); return; }
        saved = structuredClone(policy);
        draft = structuredClone(policy);
        subject = { role_id: state.policy.everyone_id };
        reviewed = "";
        impact.replaceChildren();
        render();
    };
    const inherited = () => saved.synced ? state.effective_overrides || state.parent_overrides || [] : state.parent_overrides || [];
    const shown = () => draft.synced ? inherited() : draft.overrides || [];
    const dirty = () => JSON.stringify(saved) !== JSON.stringify(draft);
    const roleByID = id => state.policy.roles.find(r => r.id === id);
    const manageable = roleID => roleID === state.policy.everyone_id || (state.manageable_role_ids || []).includes(roleID);
    const changed = () => { reviewed = ""; impact.replaceChildren(); render(); };
    const subjectKey = s => s.user_id ? `member:${s.user_id}` : `role:${s.role_id}`;
    const render = () => {
        const grantable = new Set(state.grantable_capabilities || []);
        const policy = state.policy;
        const overrides = shown();
        const locked = draft.synced;
        const role = subject.role_id ? roleByID(subject.role_id) : null;
        const subjectLocked = locked || (subject.role_id && !manageable(subject.role_id));
        const row = info => {
            const effect = effectOf(overrides, subject, info.key);
            const now = role ? t("acc.now", { state: t(roleAllowed(state, overrides, role, info.key) ? "acc.allowed" : "acc.denied") }) : null;
            return effectControl(capabilityLabel(info, lang()), effect, next => {
                draft.overrides = setWithDependencies(draft.overrides || [], state.capabilities, subject, info.key, next);
                changed();
            }, { disabled: subjectLocked || !grantable.has(info.key), hint: !grantable.has(info.key) ? t("acc.locked") : now });
        };
        const memberIDs = new Set((draft.overrides || []).filter(o => o.user_id).map(o => o.user_id));
        if (subject.user_id) memberIDs.add(subject.user_id);
        const subjects = select([
            ...policy.roles.slice().sort((a, b) => b.position - a.position).map(r => ({ value: `role:${r.id}`, label: r.name, disabled: !manageable(r.id) })),
            ...[...memberIDs].map(uid => ({ value: `member:${uid}`, label: t("acc.member", { name: names.get(uid) || t("acc.memberRef", { id: uid }) }) })),
        ], subjectKey(subject));
        subjects.onchange = () => {
            const [type, value] = subjects.value.split(":");
            subject = type === "member" ? { user_id: Number(value) } : { role_id: Number(value) };
            render();
        };
        const main = state.capabilities.filter(c => MAIN_CAPABILITIES.includes(c.key)).sort((a, b) => MAIN_CAPABILITIES.indexOf(a.key) - MAIN_CAPABILITIES.indexOf(b.key));
        const current = presetOf(overrides, policy.everyone_id);
        const canSync = !!draft.parent_id && state.parent_access_available !== false;
        const save = h("button", { class: "btn btn-primary btn-block", type: "button", disabled: !dirty() || reviewed !== JSON.stringify(draft), onclick: event => busy(event.currentTarget, async () => {
            const result = await admin.roleChange({ kind: "channel_access_set", expected_revision: policy.revision, channel: draft });
            toast(t(result.enforcement_pending ? "chan.pending" : "acc.savedNote"));
            await load();
        }) }, t("acc.save"));
        const preview = h("button", { class: "btn btn-block", type: "button", disabled: !dirty(), onclick: event => busy(event.currentTarget, async () => {
            const fingerprint = JSON.stringify(draft);
            const result = await admin.accessPreview({ change: { kind: "channel_access_set", expected_revision: policy.revision, channel: draft }, userIDs: [0] });
            if (fingerprint !== JSON.stringify(draft)) return;
            const change = c => {
                const info = state.capabilities.find(x => x.key === c.capability);
                return h("li", {}, t("acc.change", { capability: capabilityLabel(info, lang()) || c.capability, before: t(c.before ? "acc.allowed" : "acc.denied"), after: t(c.after ? "acc.allowed" : "acc.denied") }));
            };
            impact.replaceChildren(h("p", { class: "section-label" }, t("acc.impact")),
                ...(result.roles || []).map(r => h("div", { class: "card admin-impact-card" }, h("p", { class: "row-title" }, r.name),
                    r.changes?.length ? h("ul", {}, r.changes.map(change)) : h("p", { class: "hint" }, t("acc.unchanged")))),
                ...(result.members || []).filter(m => m.user_id === 0 && m.changes?.length).map(m => h("div", { class: "card admin-impact-card" },
                    h("p", { class: "row-title" }, t("acc.guests")), h("ul", {}, m.changes.map(change)))));
            reviewed = fingerprint;
            render();
        }) }, t("acc.preview"));
        status.textContent = dirty() ? (reviewed === JSON.stringify(draft) ? t("acc.unsaved") : t("acc.previewFirst")) : "";
        body.replaceChildren(
            h("div", { class: "card" },
                toggle(t("acc.synced"), draft.synced, value => {
                    draft = setSynced(draft, value, inherited());
                    changed();
                }, { disabled: !draft.synced && !canSync, hint: !draft.parent_id ? t("acc.noParent") : state.parent_access_available === false ? t("acc.parentHidden") : t("acc.syncedHint") })),
            h("p", { class: "section-label" }, t("acc.presets")),
            h("div", { class: "admin-presets" }, Object.keys(PRESETS).map(name => h("button", { type: "button", class: "btn", "aria-pressed": String(current === name), disabled: !manageable(policy.everyone_id), onclick: () => {
                // A preset customises a synced channel, starting from what it inherited.
                if (draft.synced) draft = setSynced(draft, false, inherited());
                draft.overrides = applyPreset(draft.overrides || [], policy.everyone_id, name);
                subject = { role_id: policy.everyone_id };
                changed();
            } }, t(`acc.preset.${name}`)))),
            h("p", { class: "section-label" }, t("acc.subject")),
            h("div", { class: "admin-row-fields" }, subjects,
                h("button", { class: "btn", type: "button", disabled: locked, onclick: event => busy(event.currentTarget, async () => {
                    const member = await findMember(admin, state, id);
                    if (!member) return;
                    names.set(member.user_id, member.nickname || member.unique_id);
                    subject = { user_id: member.user_id };
                    render();
                }) }, t("acc.addMember"))),
            h("div", { class: "admin-effects" }, main.map(row)),
            h("details", { class: "admin-details", open: openAll, ontoggle: event => { openAll = event.currentTarget.open; } }, h("summary", {}, t("acc.all")),
                groupCapabilities(state.capabilities, "", lang(), { channelOnly: true }).flatMap(({ items }) => items.filter(i => !MAIN_CAPABILITIES.includes(i.key)).map(row))),
            impact,
            status,
            h("div", { class: "admin-stack admin-sticky" }, preview, save,
                dirty() ? h("button", { class: "btn btn-block", type: "button", onclick: () => { draft = structuredClone(saved); changed(); } }, t("acc.discard")) : null),
            accessCheck(admin, state, id));
    };
    load();
}

/** Search sheet for a member of this channel's roster; resolves {user_id, nickname, unique_id} or null. */
async function findMember(admin, state, channelID) {
    const query = await textSheet(t("acc.checkSearch"), t("acc.checkWho"), { confirm: t("acc.checkSearch"), required: true });
    if (!query) return null;
    const page = await admin.roleMembers({ channelID, revision: state.policy.revision, search: query });
    const entries = (page.entries || []).filter(m => m.user_id > 0);
    if (!entries.length) { toast(t("roles.noMembers"), "warn"); return null; }
    const id = await chooseSheet(t("acc.checkWho"), entries.map(m => ({ value: m.user_id, label: m.nickname || m.unique_id })));
    return entries.find(m => m.user_id === id) || null;
}

function accessCheck(admin, state, channelID) {
    const who = select([{ value: 0, label: t("acc.guests") }], 0);
    const search = input({ type: "search", maxlength: "128", autocomplete: "off" });
    const found = h("p", { class: "hint", role: "status" });
    const what = select(state.capabilities.filter(c => c.channel).map(c => ({ value: c.key, label: capabilityLabel(c, lang()) })), "view_channel");
    const result = h("p", { class: "admin-result", role: "status", "aria-live": "polite" });
    const hierarchy = h("p", { class: "hint" });
    const find = h("button", { class: "btn", type: "button", onclick: event => busy(event.currentTarget, async () => {
        const page = await admin.roleMembers({ channelID, revision: state.policy.revision, search: search.value.trim() });
        const entries = page.entries || [];
        who.replaceChildren(h("option", { value: "0" }, t("acc.guests")), ...entries.filter(m => m.user_id > 0).map(m => h("option", { value: String(m.user_id) }, m.nickname || m.unique_id)));
        if (entries.length) who.value = String(entries.find(m => m.user_id > 0)?.user_id || 0);
        found.textContent = t("acc.found", { count: entries.length });
    }) }, t("acc.checkSearch"));
    const check = h("button", { class: "btn btn-primary", type: "button", onclick: event => busy(event.currentTarget, async () => {
        result.textContent = t("admin.loading");
        hierarchy.textContent = "";
        const response = await admin.accessCheck({ userID: Number(who.value), channelID, capability: what.value, revision: state.policy.revision })
            .catch(error => { result.textContent = ""; throw error; });
        const decision = response.decision || {};
        let text = t("acc.result", { state: t(decision.allowed ? "acc.allowed" : "acc.denied"), reason: t(`acc.reason.${decision.reason}`) });
        if (decision.requirement) text += ` (${capabilityLabel(state.capabilities.find(c => c.key === decision.requirement), lang()) || decision.requirement})`;
        const roles = (decision.role_ids || []).map(rid => state.policy.roles.find(r => r.id === rid)?.name).filter(Boolean);
        if (roles.length) text += ` · ${t("acc.roles", { roles: roles.join(", ") })}`;
        result.textContent = text;
        result.dataset.allowed = String(!!decision.allowed);
        if (Number(who.value) > 0) hierarchy.textContent = t(response.can_manage_member ? "acc.manageable" : "acc.notManageable");
    }) }, t("acc.checkButton"));
    who.onchange = what.onchange = () => { result.textContent = hierarchy.textContent = ""; };
    return h("section", { class: "card admin-check-card", "aria-label": t("acc.check") },
        h("h2", { class: "admin-card-title" }, t("acc.check")),
        h("p", { class: "hint" }, t("acc.checkSaved")),
        h("div", { class: "admin-row-fields" }, field(t("acc.checkSearch"), search), find), found,
        field(t("acc.checkWho"), who), field(t("acc.checkWhat"), what), check, result, hierarchy);
}

// --- Invitations ----------------------------------------------------------------------

export function renderInvites({ root, session, admin }, id) {
    const channel = channelOf(session, id);
    if (!channel) { location.hash = "#/admin"; return; }
    const body = adminScreen(root, channel.name, { sub: t("inv.title"), back: `#/admin/channel/${id}` }, loadingNote());
    let result = null, token = "";
    const access = select([{ value: "guest", label: t("inv.guest") }, { value: "member", label: t("inv.member") }], "guest");
    const render = () => {
        const link = token ? inviteLink(app.info?.public_url || location.origin, id, token) : "";
        const make = h("button", { class: "btn btn-primary btn-block", type: "button", onclick: event => busy(event.currentTarget, async () => {
            if (result.active && !await confirmSheet(t("inv.regenerate"), t("inv.regenerateAsk"), t("inv.regenerate"))) return;
            result = await admin.invite("regenerate", id, access.value);
            token = result.token || "";
            render();
            toast(t("inv.ready"));
        }) }, t(result.active ? "inv.regenerate" : "inv.create"));
        const revoke = h("button", { class: "btn btn-danger btn-block", type: "button", onclick: event => busy(event.currentTarget, async () => {
            if (!await confirmSheet(t("inv.revoke"), "", t("inv.revoke"))) return;
            result = await admin.invite("revoke", id);
            token = "";
            render();
            toast(t("inv.revoked"));
        }) }, t("inv.revoke"));
        body.replaceChildren(
            h("p", { class: "hint" }, t("inv.help")),
            h("p", { class: "admin-result", role: "status" }, result.active ? (token ? t("inv.ready") : t("inv.active", { access: t(result.access === "member" ? "inv.member" : "inv.guest") })) : t("inv.none")),
            link ? copyBox(t("inv.link"), link, { share: t("inv.shareTitle", { name: channel.name }) }) : null,
            field(t("inv.access"), access),
            h("div", { class: "admin-stack" }, make, result.active ? revoke : null));
    };
    admin.invite("get", id).then(r => {
        result = r;
        if (r.access) access.value = r.access;
        render();
    }, error => unavailable(body, error));
}

// --- Webhooks ---------------------------------------------------------------------------

export function renderWebhooks({ root, session, admin }, id) {
    const channel = channelOf(session, id);
    if (!channel) { location.hash = "#/admin"; return; }
    const body = adminScreen(root, channel.name, { sub: t("hook.title"), back: `#/admin/channel/${id}` }, loadingNote());
    let hooks = [], secret = null;
    const name = input({ maxlength: "60", required: true, autocomplete: "off" });
    const render = () => {
        const submit = h("button", { class: "btn btn-primary", type: "submit" }, t("hook.create"));
        body.replaceChildren(
            h("p", { class: "hint" }, t("hook.help")),
            secret ? h("div", { class: "card admin-stack admin-secret", role: "alert" },
                h("p", { class: "row-title admin-wrap" }, t("hook.secret")),
                copyBox(t("hook.endpoint"), `/hooks/${secret.id}`),
                copyBox(t("hook.token"), secret.token),
                secret.port ? h("p", { class: "hint" }, t("hook.port", { port: secret.port })) : null) : null,
            hooks.length ? h("ul", { class: "list" }, hooks.map(hook => h("li", { class: "row" },
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, hook.name), h("span", { class: "row-sub" }, t("hook.created", { date: when(hook.created_at) }))),
                h("button", { class: "btn btn-danger", type: "button", onclick: event => busy(event.currentTarget, async () => {
                    if (!await confirmSheet(t("hook.revoke"), t("hook.revokeAsk", { name: hook.name }), t("hook.revoke"))) return;
                    hooks = (await admin.webhooks("revoke", id, { id: hook.id })).hooks || [];
                    if (secret?.id === hook.id) secret = null;
                    toast(t("hook.revoked"));
                    render();
                }) }, t("hook.revoke"))))) : emptyNote(t("hook.empty")),
            h("form", { class: "admin-row-fields admin-gap", onsubmit: event => {
                event.preventDefault();
                const value = name.value.trim();
                if (!value) { name.focus(); return; }
                busy(submit, async () => {
                    const result = await admin.webhooks("create", id, { name: value });
                    hooks = result.hooks || [];
                    secret = { id: result.id, token: result.token, port: result.health_port };
                    name.value = "";
                    render();
                });
            } }, field(t("hook.name"), name), submit));
    };
    admin.webhooks("list", id).then(r => { hooks = r.hooks || []; render(); }, error => unavailable(body, error));
    return () => { secret = null; };
}

