// Profile actions address an ACCOUNT, not a device (521). The server never
// sends user_id, so the account is the unique_id; client_id names one session
// and on_mobile marks phones. Session-bound actions (Client Info, pokes,
// per-session audio) resolve to one explicitly chosen primary session.
import { accountKey, deviceKind, groupSessionsByAccount } from "./member-presence.js";

function describeAccount(clients, key, selfID, clicked) {
    const sessions = clients.filter(client => accountKey(client) === key);
    if (!sessions.length) return null;
    const group = groupSessionsByAccount(sessions, selfID)[0];
    return {
        key,
        accountID: group.primary.unique_id || "",
        primary: group.primary,
        sessions: group.sessions,
        others: group.others,
        multiple: group.sessions.length > 1,
        primaryDevice: deviceKind(group.primary),
        clicked: clicked || group.primary,
    };
}

// resolveAccountTarget maps the session a user clicked to its account and
// primary session. Unknown sessions resolve to null so callers keep the row.
export function resolveAccountTarget(clients, clientID, selfID) {
    const clicked = clients.find(client => client.client_id === clientID);
    return clicked ? describeAccount(clients, accountKey(clicked), selfID, clicked) : null;
}

export function resolveAccountTargetByUID(clients, uniqueID, selfID) {
    if (!uniqueID) return null;
    const session = clients.find(client => client.unique_id === uniqueID);
    return session ? describeAccount(clients, accountKey(session), selfID) : null;
}

// primarySessionLabel names the chosen session in dialogs that necessarily
// show one device, e.g. "Primary session: Desktop (2 devices connected)".
export function primarySessionLabel(target, translate) {
    if (!target?.multiple) return "";
    return translate("account.primarySession", { device: translate("presence.device." + target.primaryDevice), count: target.sessions.length });
}
