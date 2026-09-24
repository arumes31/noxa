import { test as base, expect } from "@playwright/test";

// Install before test hooks/navigation, and observe the whole context so errors
// in secondary windows cannot silently bypass the default page's checks.
export const test = base.extend({
    runtimeErrors: [async ({ context }, use) => {
        const errors = [];
        const contexts = new Set();
        const onError = event => {
            const error = event.error();
            const url = event.page()?.url() || "<closed page>";
            errors.push(`${url}\n${error.stack || error.message}`);
        };
        const observe = target => {
            if (contexts.has(target)) return;
            contexts.add(target);
            target.on("weberror", onError);
        };
        observe(context);
        try {
            await use(observe);
        } finally {
            for (const target of contexts) target.off("weberror", onError);
            expect(errors, "Unexpected browser runtime errors").toEqual([]);
        }
    }, { auto: true }],
    newIsolatedPage: async ({ browser, runtimeErrors }, use) => {
        const contexts = [];
        try {
            await use(async options => {
                const context = await browser.newContext(options);
                contexts.push(context);
                runtimeErrors(context);
                return context.newPage();
            });
        } finally {
            await Promise.all(contexts.map(context => context.close()));
        }
    },
});

export { expect };
