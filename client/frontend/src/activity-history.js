// Ephemeral, paged history for the Activity workspace. Every page goes through
// the existing authenticated history APIs; nothing is written to a new store.
export function createActivityHistory({ listSources, loadPage, current, changed = () => {}, pause = () => new Promise(resolve => setTimeout(resolve, 350)) }) {
    const sources = new Map();
    let revision = 0, disposed = false, pending = null, loading = false, failed = false;
    const valid = token => !disposed && current() && token === revision;
    const notify = () => { if (!disposed && current()) changed(); };
    async function run(older) {
        if (disposed || !current()) return;
        if (pending) return pending;
        const token = ++revision;
        loading = true; failed = false; notify();
        const operation = (async () => {
            try {
                const listed = await listSources();
                if (!valid(token)) return;
                const inventory = Array.isArray(listed) ? listed : listed.sources;
                failed = !!listed.failed;
                const allowed = new Set(inventory.map(source => source.key));
                for (const key of sources.keys()) if (!allowed.has(key)) sources.delete(key);
                notify();
                for (const source of inventory) {
                    if (!valid(token)) return;
                    const previous = sources.get(source.key);
                    if (older && previous && !previous.more) continue;
                    await pause();
                    if (!valid(token)) return;
                    try {
                        const page = await loadPage(source, older ? previous?.cursor || 0 : 0);
                        if (!valid(token)) return;
                        if (page.denied) { sources.delete(source.key); notify(); continue; }
                        const complete = !older && !page.more;
                        const rows = new Map(complete ? [] : previous?.rows || []);
                        for (const entry of page.entries) rows.set(entry.key, entry);
                        // If new traffic jumped over our previous newest row, resume
                        // at the new page's tail so older pagination cannot skip a gap.
                        const gap = !older && previous && page.cursor > previous.newest;
                        sources.set(source.key, { source, rows,
                            newest: Math.max(complete ? 0 : previous?.newest || 0, ...page.entries.map(entry => entry.cursor || 0)),
                            cursor: older || !previous || gap || complete ? page.cursor : previous.cursor,
                            more: older || !previous || gap || complete ? page.more : previous.more,
                        });
                    } catch {
                        if (!valid(token)) return;
                        sources.delete(source.key); failed = true;
                    }
                    notify();
                }
            } catch { if (valid(token)) { sources.clear(); failed = true; } }
            finally { if (valid(token)) { pending = null; loading = false; notify(); } }
        })();
        pending = operation;
        return operation;
    }
    return {
        refresh: () => run(false), older: () => run(true),
        entries: () => current() && !disposed ? [...sources.values()].flatMap(source => [...source.rows.values()]) : [],
        status: () => ({ loading, failed, more: [...sources.values()].some(source => source.more) }),
        invalidate() { revision++; sources.clear(); pending = null; loading = false; failed = false; notify(); },
        remove(predicate) { for (const source of sources.values()) for (const [key, entry] of source.rows) if (predicate(entry)) source.rows.delete(key); notify(); },
        dispose() { disposed = true; revision++; sources.clear(); pending = null; },
    };
}
