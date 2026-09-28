import { expect, test } from "./fixtures.js";

test("screen audio preferences persist independently of voice and notify live receivers", async ({ page }) => {
    await page.route("**/share-volume-fixture", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Share volume</title>" }));
    await page.goto("/share-volume-fixture");
    const result = await page.evaluate(async () => {
        let saved = { user_volumes: { alice: 35 }, muted_users: ["alice"] };
        window.__noxa = { state: { settings: structuredClone(saved) } };
        window.go = { main: { App: { SaveSettings: async value => { saved = structuredClone(value); return ""; }, GetSettings: async () => structuredClone(saved) } } };
        const audio = await import("/src/audio.js");
        const seen = [];
        const unsubscribe = audio.onShareAudioChange(uid => seen.push([uid, audio.getUserShareVolume(uid), audio.isUserShareMuted(uid)]));
        const initial = [audio.getUserShareVolume("alice"), audio.isUserShareMuted("alice")];
        await audio.setUserShareVolume("alice", 160);
        await audio.setUserShareMuted("alice", true);
        window.__noxa.state.settings = await window.go.main.App.GetSettings();
        const reloaded = [audio.getUserShareVolume("alice"), audio.isUserShareMuted("alice"), audio.getUserVolume("alice"), audio.isUserMuted("alice")];
        await audio.setUserShareVolume("alice", 900);
        unsubscribe();
        window.__noxa.state.settings.blocked_users = ["bob"];
        return { initial, reloaded, seen, bounded: audio.getUserShareVolume("alice"), blocked: audio.isUserShareMuted("bob"), saved };
    });
    expect(result.initial).toEqual([1, false]);
    expect(result.reloaded).toEqual([1.6, true, 0.35, true]);
    expect(result.seen).toEqual([["alice", 1.6, false], ["alice", 1.6, false], ["alice", 1.6, true], ["alice", 1.6, true], ["alice", 2, true], ["alice", 2, true]]);
    expect(result.bounded).toBe(2);
    expect(result.blocked).toBe(true);
    expect(result.saved.user_volumes).toEqual({ alice: 35 });
    expect(result.saved.muted_users).toEqual(["alice"]);
});

test("failed share preference saves roll back immediately without changing the microphone", async ({ page }) => {
    await page.route("**/share-volume-fixture", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Share volume</title>" }));
    await page.goto("/share-volume-fixture");
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { user_share_volumes: { alice: 65 }, user_volumes: { alice: 80 } } } };
        let release;
        window.go = { main: { App: { SaveSettings: () => new Promise(resolve => { release = resolve; }) } } };
        const audio = await import("/src/audio.js");
        const save = audio.setUserShareVolume("alice", 20);
        const preview = audio.getUserShareVolume("alice");
        await Promise.resolve(); release("disk full");
        const error = await save.then(() => "", failure => failure.message);
        return { preview, error, restored: audio.getUserShareVolume("alice"), voice: audio.getUserVolume("alice") };
    });
    expect(result).toEqual({ preview: 0.2, error: "disk full", restored: 0.65, voice: 0.8 });
});
