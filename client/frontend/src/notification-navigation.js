const messageID = value => Number.isSafeInteger(Number(value)) && Number(value) > 0 ? Number(value) : 0;

// Store only a destination, never message content or attachment credentials.
export function captureNotificationDestination(event, context, state) {
    const tabID = context.tabID || state.activeTabID;
    const identityUID = context.identityUID || (tabID === state.activeTabID ? state.myUniqueID : "");
    if (!tabID || !identityUID) return null;
    const source = context.reference || (context.channelID !== undefined
        ? { kind: "channel", channel_id: context.channelID }
        : context.uid ? { kind: "dm", peer_id: context.uid } : null);
    if (!source) return null;
    let reference;
    if (source.kind === "dm" && typeof source.peer_id === "string" && source.peer_id) {
        reference = { kind: "dm", peer_id: source.peer_id, client_message_id: String(source.client_message_id || ""), local_seq: messageID(source.local_seq) };
    } else if (source.kind === "group" && typeof source.group_id === "string" && source.group_id) {
        reference = { kind: "group", group_id: source.group_id, message_id: messageID(source.message_id) };
    } else if (["channel", "thread"].includes(source.kind) && Number.isSafeInteger(Number(source.channel_id)) && Number(source.channel_id) >= 0) {
        reference = { kind: source.kind, channel_id: Number(source.channel_id), message_id: messageID(source.message_id) };
        if (source.kind === "thread") {
            if (!reference.channel_id || !messageID(source.thread_id)) return null;
            reference.thread_id = messageID(source.thread_id);
        }
    } else return null;
    return Object.freeze({ tabID, identityUID, address: context.address || (tabID === state.activeTabID ? state.lastConnect?.addr || "" : ""), reference: Object.freeze(reference) });
}

// Activation is an existing-tab operation. The backend replay must finish
// before any destination can use the newly active server's stores or identity.
export function createNotificationNavigator({ state, app, on, open, unavailable, confirmSwitch = async () => true }) {
    let sequence = 0, cancelActivation = null;
    const scope = () => {
        const current = state();
        return [current.activeTabID, current.serverGeneration, current.sessionGeneration, current.myUniqueID];
    };
    const same = captured => captured.every((value, index) => value === scope()[index]);
    return async destination => {
        const token = ++sequence;
        cancelActivation?.();
        if (!destination) return false;
        let captured = scope();
        const current = () => token === sequence && same(captured);
        try {
            const tabs = await app().ListTabs();
            if (!current()) return false;
            const target = tabs?.find(tab => tab.id === destination.tabID);
            if (!target?.connected || destination.address && target.addr !== destination.address) { unavailable(); return false; }
            if (state().activeTabID !== destination.tabID) {
                if (!await confirmSwitch(target) || !current()) return false;
                const refreshed = (await app().ListTabs())?.find(tab => tab.id === destination.tabID);
                if (!current()) return false;
                if (!refreshed?.connected || destination.address && refreshed.addr !== destination.address) { unavailable(); return false; }
                let finish;
                const replay = new Promise(resolve => { finish = resolve; });
                let reset = false, settled = false;
                const cleanup = [];
                const done = ready => {
                    if (settled) return;
                    settled = true; clearTimeout(timer);
                    cleanup.forEach(stop => stop?.());
                    if (cancelActivation === cancel) cancelActivation = null;
                    finish(ready);
                };
                const cancel = () => done(false);
                const timer = setTimeout(() => {
                    if (token === sequence && (current() || reset && state().activeTabID === destination.tabID)) unavailable();
                    cancel();
                }, 5000);
                cancelActivation = cancel;
                cleanup.push(on("tab_reset", tabID => {
                    if (reset || tabID !== destination.tabID) { cancel(); return; }
                    reset = true;
                }));
                cleanup.push(on("tab_replay_done", tabID => {
                    if (reset && tabID === destination.tabID) { captured = scope(); done(true); }
                }));
                try {
                    const error = await app().SetActiveTab(destination.tabID);
                    if (error) throw new Error(String(error));
                }
                catch (error) { cancel(); throw error; }
                if (!await replay || !current() || state().activeTabID !== destination.tabID) return false;
            }
            const session = await app().SessionInfoForTab(destination.tabID);
            if (!current()) return false;
            const owner = await app().DMHistoryContextForTab(destination.tabID);
            if (!current()) return false;
            if (!session?.connected || owner?.tab_id !== destination.tabID || owner.identity_uid !== destination.identityUID) { unavailable(); return false; }
            // The replay marker may beat tabs.js's own session lookup. Group
            // membership needs the authenticated roster ID, not the local key.
            if (typeof session.client_id === "string") state().myClientID = session.client_id;
            await open(destination.reference, current);
            return true;
        } catch {
            if (current()) unavailable();
            return false;
        }
    };
}
