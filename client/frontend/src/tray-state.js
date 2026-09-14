// Serialize state transitions across the native bridge. Repeated tree renders
// do no work; while a call is pending, only the newest state is retained.
export function createTrayVoiceSync(send) {
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
    return (speaking, muted, deafened) => {
        desired = [speaking, muted, deafened];
        void flush();
    };
}
