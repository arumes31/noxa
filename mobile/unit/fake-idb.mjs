// fake-idb.mjs — just enough of IndexedDB for core/db.js in Node: named
// databases of object stores kept in memory, requests that complete on the
// next tick.
export function installFakeIndexedDB() {
    const databases = new Map();
    const done = fn => { const request = {}; request.result = fn(); return request; };
    globalThis.indexedDB = {
        open(name) {
            const request = {};
            setTimeout(() => {
                if (!databases.has(name)) databases.set(name, new Map());
                const stores = databases.get(name);
                request.result = {
                    objectStoreNames: { contains: store => stores.has(store) },
                    createObjectStore: store => stores.set(store, new Map()),
                    transaction(storeName) {
                        const store = stores.get(storeName);
                        const tx = {
                            objectStore: () => ({
                                get: key => done(() => store.get(key)),
                                put: (value, key) => done(() => { store.set(key, value); }),
                                delete: key => done(() => { store.delete(key); }),
                                getAllKeys: () => done(() => [...store.keys()]),
                                clear: () => done(() => store.clear()),
                            }),
                        };
                        setTimeout(() => tx.oncomplete?.());
                        return tx;
                    },
                    close() {},
                };
                request.onupgradeneeded?.();
                request.onsuccess?.();
            });
            return request;
        },
    };
    return () => databases.clear();
}
