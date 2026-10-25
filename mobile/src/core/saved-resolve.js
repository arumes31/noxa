// saved-resolve.js — turns a saved reference (core/saved.js) back into its
// message. The sources re-read the conversation the way the screens do
// (channel history pages, thread history, group pages, the local direct
// message store), so a message the user can no longer see resolves to
// nothing and is shown as unavailable.

/** The conversation tag of a reference, as app.unread uses. */
export function referenceTag(ref) {
    if (ref.kind === "dm") return `dm:${ref.peer}`;
    if (ref.kind === "group") return `conversation:${ref.groupID}`;
    if (ref.kind === "thread") return `thread:${ref.channelID}:${ref.threadID}`;
    return `channel:${ref.channelID}`;
}

/** The route that opens a reference's conversation. */
export function referenceHref(ref) {
    if (ref.kind === "dm") return `#/dm/${encodeURIComponent(ref.peer)}`;
    if (ref.kind === "group") return `#/group/${encodeURIComponent(ref.groupID)}`;
    if (ref.kind === "thread") return `#/thread/${ref.channelID}/${ref.threadID}`;
    return `#/chat/${ref.channelID}`;
}

/** The message ID a reference points at inside its conversation. */
export function referenceMessageID(ref) {
    return ref.kind === "dm" ? ref.clientMsgID : ref.messageID;
}

/**
 * Resolves one reference. sources has channel(channelID, id),
 * thread(channelID, threadID, id), group(groupID, id) and dm(peer, id), each
 * returning (a promise of) {from, text, at} or null. Errors resolve to
 * unavailable rather than failing the list.
 */
export async function resolveReference(ref, sources) {
    const lookup = {
        channel: () => sources.channel?.(ref.channelID, ref.messageID),
        thread: () => sources.thread?.(ref.channelID, ref.threadID, ref.messageID),
        group: () => sources.group?.(ref.groupID, ref.messageID),
        dm: () => sources.dm?.(ref.peer, ref.clientMsgID),
    }[ref.kind];
    let message = null;
    try {
        message = (await lookup?.()) || null;
    } catch {
        message = null;
    }
    if (message && (message.deleted || typeof message.text !== "string")) message = null;
    return { reference: ref, message, href: referenceHref(ref), tag: referenceTag(ref) };
}

/** Resolves a list in order; one at a time by default, because history requests of one scope must not overlap. */
export async function resolveAll(refs, sources, parallel = 1) {
    const out = new Array(refs.length);
    let next = 0;
    const worker = async () => {
        while (next < refs.length) {
            const index = next++;
            out[index] = await resolveReference(refs[index], sources);
        }
    };
    await Promise.all(Array.from({ length: Math.min(parallel, refs.length) }, worker));
    return out;
}
