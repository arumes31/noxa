// invite.js — opens a channel invitation link (#/invite/<channel>/<token>).
// The server confirms the token and the user's access; the link never
// bypasses the channel's permissions or password. Text channels open their
// chat; voice channels are offered to join.
import { app, joinVoice } from "../app.js";
import { adminFor, parseInvite } from "../core/admin.js";
import { addStrings, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { header, nav } from "./common.js";

addStrings({
    en: { "invite.title": "Invitation", "invite.checking": "Checking the invitation…", "invite.invalid": "This invitation link is not valid.", "invite.failed": "This invitation cannot be used: {error}", "invite.join": "Join {name}", "invite.voice": "You were invited to the voice channel {name}." },
    de: { "invite.title": "Einladung", "invite.checking": "Einladung wird geprüft…", "invite.invalid": "Dieser Einladungslink ist ungültig.", "invite.failed": "Diese Einladung kann nicht genutzt werden: {error}", "invite.join": "{name} beitreten", "invite.voice": "Du wurdest in den Sprachkanal {name} eingeladen." },
});

export function renderInvite(root, arg) {
    const session = app.session;
    const body = h("div", { class: "screen-body" }, h("p", { class: "hint", role: "status" }, t("invite.checking")));
    root.replaceChildren(h("section", { class: "screen" }, header(t("invite.title"), { back: "#/home" }), body, nav("servers")));
    const invite = parseInvite(arg);
    if (!invite) { body.replaceChildren(h("p", { class: "error" }, t("invite.invalid"))); return; }
    adminFor(session).resolveInvite(invite.channelID, invite.token).then(async () => {
        if (app.session !== session) return;
        const channel = session.channels().find(c => c.id === invite.channelID);
        if (channel?.textOnly || (channel && channel.chat && !channel.canConnect)) {
            await session.subscribe([channel.id]).catch(() => {});
            location.replace(`#/chat/${channel.id}`);
            return;
        }
        const name = channel?.name || `#${invite.channelID}`;
        const join = h("button", { class: "btn btn-primary btn-block", type: "button", onclick: async () => {
            join.disabled = true;
            try {
                await joinVoice(invite.channelID);
                location.replace("#/voice");
            } catch (error) {
                toast(t("voice.joinFailed", { error: error.message }), "warn");
                join.disabled = false;
            }
        } }, t("invite.join", { name }));
        body.replaceChildren(h("p", { class: "hint" }, t("invite.voice", { name })), join,
            channel?.chat ? h("a", { class: "btn btn-block", href: `#/chat/${channel.id}`, style: "margin-top:8px" }, `# ${name}`) : null);
    }, error => {
        body.replaceChildren(h("p", { class: "error" }, t("invite.failed", { error: error.message })));
    });
}
