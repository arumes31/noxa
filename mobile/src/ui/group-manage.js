// group-manage.js — the private group sheet: members with roles, inviting,
// removing, handing over ownership, renaming, muting, leaving and deleting.
// Owner-only actions are shown to the owner only (the server enforces them).
import { app } from "../app.js";
import { MAX_GROUP_MEMBERS } from "../core/groups.js";
import { t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { muteToggle, panel } from "./conversation-tools.js";
import { visibleMembers } from "./new-chat.js";

export function memberName(uid) {
    if (uid === app.session?.uniqueID) return app.session.nickname;
    return app.session?.clients().find(c => c.unique_id === uid)?.nickname || app.dm?.conversations.get(uid)?.nickname || t("dm.someone");
}

const fail = error => toast(t("common.error", { error: error.message }), "warn");

async function confirm(title, body, label) {
    return sheet(title, body, [{ label: t("link.cancel"), value: false }, { label, value: true, danger: true }]);
}

/** Opens the sheet for group id; onGone runs after leaving or deleting it. */
export function showGroupManage(id, { onGone } = {}) {
    const groups = app.groups;
    const session = app.session;
    const body = h("div", { class: "group-manage" });
    let inviting = false;
    let busy = false;
    const view = panel(groups.list.get(id)?.name || t("group.title"), body, { label: t("groupManage.title") });

    const run = async work => {
        if (busy) return;
        busy = true;
        body.setAttribute("aria-busy", "true");
        try { await work(); } catch (error) { fail(error); }
        busy = false;
        body.removeAttribute("aria-busy");
        render();
    };

    function memberRow(member, group, owner) {
        const isOwner = member.unique_id === group.owner;
        const self = member.unique_id === session.uniqueID;
        const name = memberName(member.unique_id);
        const badges = [isOwner ? h("span", { class: "role-badge owner" }, t("groupManage.owner")) : null,
            member.pending ? h("span", { class: "role-badge" }, t("groupManage.invited")) : null,
            self ? h("span", { class: "role-badge" }, t("dm.you")) : null];
        const actions = [];
        if (owner && !self) {
            if (!member.pending) actions.push(h("button", { class: "btn btn-small", type: "button", onclick: () => run(async () => {
                if (await confirm(t("groupManage.transferTitle", { name }), t("groupManage.transferBody", { name }), t("groupManage.transfer"))) await groups.transfer(id, member.unique_id);
            }) }, t("groupManage.transfer")));
            actions.push(h("button", { class: "btn btn-small btn-danger", type: "button", onclick: () => run(async () => {
                const key = member.pending ? "withdraw" : "remove";
                if (await confirm(t(`groupManage.${key}Title`, { name }), t(`groupManage.${key}Body`, { name }), t(`groupManage.${key}`))) await groups.remove(id, member.unique_id);
            }) }, t(member.pending ? "groupManage.withdraw" : "groupManage.remove")));
        }
        return h("li", { class: "member-row" },
            avatar(name, "sm"),
            h("span", { class: "row-main" }, h("span", { class: "row-title" }, name), h("span", { class: "member-badges" }, badges)),
            actions.length ? h("span", { class: "member-actions" }, actions) : null);
    }

    function invitePicker(group) {
        const inGroup = new Set((group.members || []).map(m => m.unique_id));
        const room = MAX_GROUP_MEMBERS - (group.members || []).length;
        const candidates = visibleMembers(session).filter(member => !inGroup.has(member.unique_id));
        const chosen = new Set();
        const send = h("button", { class: "btn btn-primary", type: "button", disabled: true }, t("groupManage.sendInvites"));
        const boxes = candidates.map(member => {
            const box = h("input", { type: "checkbox", onchange: event => {
                if (event.target.checked) chosen.add(member.unique_id); else chosen.delete(member.unique_id);
                send.disabled = !chosen.size;
                for (const other of body.querySelectorAll(".invite-list input")) other.disabled = !other.checked && chosen.size >= room;
            } });
            return h("li", {}, h("label", { class: "row" }, box, avatar(member.nickname, "sm"), h("span", { class: "row-title" }, member.nickname)));
        });
        send.onclick = () => run(async () => {
            for (const uid of chosen) await groups.invite(id, uid);
            inviting = false;
            toast(t("groupManage.invitesSent", { count: chosen.size }));
        });
        return h("div", { class: "card invite-picker" },
            h("p", { class: "hint" }, room > 0 ? t("groupManage.inviteHint", { count: room }) : t("groupManage.full")),
            candidates.length ? h("ul", { class: "list invite-list" }, boxes) : h("p", { class: "hint" }, t("groupManage.noCandidates")),
            h("div", { class: "group-manage-actions" }, send, h("button", { class: "btn", type: "button", onclick: () => { inviting = false; render(); } }, t("link.cancel"))));
    }

    function render() {
        const group = groups.list.get(id);
        if (!group) {
            view.close();
            return;
        }
        const owner = group.owner === session.uniqueID;
        const members = [...(group.members || [])].sort((a, b) => (b.unique_id === group.owner) - (a.unique_id === group.owner) || a.pending - b.pending || memberName(a.unique_id).localeCompare(memberName(b.unique_id)));
        const others = members.filter(m => m.unique_id !== session.uniqueID);
        const nodes = [];
        if (owner) {
            const nameInput = h("input", { class: "input", value: group.name, maxlength: "80", "aria-label": t("group.name") });
            nodes.push(h("form", { class: "rename-form", onsubmit: event => {
                event.preventDefault();
                const next = nameInput.value.trim();
                if (!next || next === group.name) return;
                run(async () => { await groups.rename(id, next); view.node.querySelector(".sheet-title").textContent = next; toast(t("groupManage.renamed")); });
            } }, nameInput, h("button", { class: "btn", type: "submit" }, t("groupManage.rename"))));
        }
        nodes.push(h("p", { class: "section-label" }, t("groupManage.members", { count: members.filter(m => !m.pending).length })),
            h("ul", { class: "list member-list" }, members.map(member => memberRow(member, group, owner))));
        if (owner) {
            nodes.push(inviting ? invitePicker(group)
                : h("button", { class: "btn btn-block", type: "button", disabled: members.length >= MAX_GROUP_MEMBERS, onclick: () => { inviting = true; render(); body.querySelector(".invite-list input, .invite-picker .btn")?.focus(); } }, t("groupManage.invite")));
        }
        nodes.push(muteToggle(`conversation:${id}`));
        // An owner hands the group over before leaving it to others.
        const mustTransfer = owner && others.length > 0;
        nodes.push(h("button", { class: "btn btn-block btn-danger", type: "button", disabled: mustTransfer, onclick: () => run(async () => {
            if (!await confirm(t("groupManage.leaveTitle"), t("groupManage.leaveBody", { name: group.name }), t("groupManage.leave"))) return;
            await groups.leave(id);
            view.close();
            onGone?.();
        }) }, t("groupManage.leave")));
        if (mustTransfer) nodes.push(h("p", { class: "hint" }, t("groupManage.transferFirst")));
        if (owner) nodes.push(h("button", { class: "btn btn-block btn-danger", type: "button", onclick: () => run(async () => {
            if (!await confirm(t("groupManage.deleteTitle"), t("groupManage.deleteBody", { name: group.name }), t("groupManage.delete"))) return;
            await groups.deleteGroup(id);
            view.close();
            onGone?.();
        }) }, t("groupManage.delete")));
        body.replaceChildren(...nodes);
    }

    const onList = () => { if (!busy && !inviting) render(); };
    groups.addEventListener("list", onList);
    view.closed.then(() => groups.removeEventListener("list", onList));
    render();
    groups.get(id).catch(() => {});
}
