import { createSafeImage } from './safe-media.js';

async function thumbnail(source) {
    const image = createSafeImage(source);
    if (!image) return '';
    await image.decode();
    const size = Math.min(image.naturalWidth, image.naturalHeight);
    if (!size) return '';
    const canvas = document.createElement('canvas');
    canvas.width = canvas.height = 64;
    canvas.getContext('2d').drawImage(image, (image.naturalWidth - size) / 2, (image.naturalHeight - size) / 2, size, size, 0, 0, 64, 64);
    const data = canvas.toDataURL('image/png');
    return data.length <= 32768 ? data : '';
}

// Decode once per visible avatar, outside the native animation loop. Old server
// or participant entries are discarded; no remote URL reaches the native layer.
export function createOverlayAvatars() {
    const cache = new Map();
    let scope = '';
    return (snapshot, state) => {
        const next = JSON.stringify([state.activeTabID, state.serverGeneration]);
        if (scope !== next || !snapshot.active) { cache.clear(); scope = next; }
        const speakers = snapshot.speakers || [];
        const ids = new Set(speakers.map(speaker => speaker.id));
        for (const id of cache.keys()) if (!ids.has(id)) cache.delete(id);
        for (const speaker of speakers) {
            if (!speaker.id) continue;
            const source = state.avatars?.get(speaker.id);
            if (!source) { void window.__noxa.fetchAvatar?.(speaker.id); continue; }
            let entry = cache.get(speaker.id);
            if (!entry || entry.source !== source) {
                entry = { source, avatar: '' }; cache.set(speaker.id, entry);
                void thumbnail(source).then(avatar => { entry.avatar = avatar; }).catch(() => {});
            }
            speaker.avatar = entry.avatar;
        }
        return snapshot;
    };
}
