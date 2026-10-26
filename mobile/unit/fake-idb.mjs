// This lightweight fixture supports request callbacks/multiple stores used by
// existing application units. Durability/concurrency evidence uses Chromium.
export function installFakeIndexedDB() {
    const databases = new Map();
    globalThis.indexedDB = {
        open(name) {
            const request = {};
            setTimeout(() => {
                if (!databases.has(name)) databases.set(name, new Map());
                const stores = databases.get(name);
                request.result = {
                    objectStoreNames: { contains: store => stores.has(store) },
                    createObjectStore: store => stores.set(store, new Map()),
                    transaction(names) {
                        let pending = 0, aborted = false, completion;
                        const tx = { abort() { aborted = true; clearTimeout(completion); setTimeout(() => tx.onabort?.()); } };
                        const finish = () => { clearTimeout(completion); completion = setTimeout(() => { if (!pending && !aborted) tx.oncomplete?.(); }); };
                        const operation = fn => {
                            const req = {}; pending++;
                            setTimeout(() => {
                                if (aborted) return;
                                req.result = fn(); req.onsuccess?.(); pending--; finish();
                            });
                            return req;
                        };
                        tx.objectStore = name => {
                            const store = stores.get(name || (Array.isArray(names) ? names[0] : names));
                            return {
                                get: key => operation(() => store.get(key)),
                                put: (value, key) => operation(() => store.set(key, value)),
                                add: (value, key) => operation(() => { if (store.has(key)) throw new Error('duplicate fixture key'); store.set(key, value); }),
                                delete: key => operation(() => store.delete(key)),
                                getAllKeys: () => operation(() => [...store.keys()]),
                                count: () => operation(() => store.size),
                                clear: () => operation(() => store.clear()),
                            };
                        };
                        finish(); return tx;
                    },
                    close() {},
                };
                request.onupgradeneeded?.(); request.onsuccess?.();
            });
            return request;
        },
    };
    return () => databases.clear();
}
