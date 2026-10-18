// Serialize state transitions across the native bridge. Repeated tree renders
// do no work; while a call is pending, only the newest state is retained.
export function createTraySync(send) {
    let desired = null;
    let sent = "";
    let pending = false;
    async function flush() {
        if (pending || !desired || desired.join() === sent) return;
        const next = desired;
        const key = next.join();
        pending = true;
        try {
            await send(...next);
            sent = key;
        } catch {
            // A later state/render may retry; never spin on a failed bridge.
        } finally {
            pending = false;
            if (desired.join() !== key) void flush();
        }
    }
    return (...state) => {
        desired = state;
        void flush();
    };
}

// (speaking, muted, deafened, inChannel) -> App.SetTrayVoiceState.
export const createTrayVoiceSync = createTraySync;

// trayInChannel mirrors syncTrayVoice's voice-session test: a live peer
// connection in a channel, outside a tab-switch journal replay.
export function trayInChannel(state) {
    return !!(state.pc && state.myChannelID && !state.replayingTabID);
}

// The tray's unread counter sums named sources that each own their count
// (active-tab chats, private groups, background server tabs) plus messages
// that arrived in the open chat while the window was not focused. Only a
// changed total crosses the bridge; no timer or polling is involved.
export function createTrayUnreadModel(send) {
    const sources = new Map();
    let unseen = 0;
    const publish = createTraySync(send);
    const total = () => {
        let count = unseen;
        for (const value of sources.values()) count += value;
        return count;
    };
    const update = () => publish(Math.min(total(), 9999));
    return {
        set(source, count) {
            const value = Math.max(0, Math.floor(Number(count) || 0));
            if (sources.get(source) === value) return;
            sources.set(source, value);
            update();
        },
        noteUnseen() {
            unseen++;
            update();
        },
        clearUnseen() {
            if (!unseen) return;
            unseen = 0;
            update();
        },
        total,
    };
}
