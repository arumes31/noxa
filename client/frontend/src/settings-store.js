import { updateStartupBlocked } from "./update-transition.js";
// Serialize immediate preference edits and publish only the persisted result.
// Settings dialogs retain their own baseline for backend conflict detection.
let pending = Promise.resolve();

export function updateLocalSettings(mutate) {
    const operation = pending.then(async () => {
        const state = window.__noxa.state;
        if (updateStartupBlocked()) return state.settings;
        const next = structuredClone(state.settings || {});
        mutate(next);
        const app = window.go.main.App;
        const error = await app.SaveSettings(next);
        if (error) throw new Error(error);
        state.settings = await app.GetSettings();
        return state.settings;
    });
    pending = operation.catch(() => {});
    return operation;
}
