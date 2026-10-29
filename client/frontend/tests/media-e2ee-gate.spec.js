import { expect, test } from "./fixtures.js";

// Review 2a-fix2 repro: when a remote offer creates several receivers, an app
// ontrack handler that reconciles ALL transceivers (remote-media.js reconcile,
// PWA Voice#reconcile) runs during the FIRST track event, before protectPeer has
// seen the later receivers. The gate reports them as unknown (undefined), which
// both call sites treat as "not gated", and their tracks are still enabled with
// no transform installed.
for (const api of ["script", "streams"]) test(`the gate fails closed for receivers not protected yet (${api})`, async ({ page }) => {
    await page.route("**/__gate__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>gate</title>" }));
    await page.goto("/__gate__");
    const seen = await page.evaluate(async api => {
        const { MediaE2EE } = await import("/src/media-e2ee.js");
        const rx = new MediaE2EE({ api });
        await rx.ready;
        const canvas = document.createElement("canvas"); canvas.width = 160; canvas.height = 90; canvas.getContext("2d").fillRect(0, 0, 160, 90);
        const ac = new AudioContext(); const dest = ac.createMediaStreamDestination(); ac.createOscillator().connect(dest);
        const source = new RTCPeerConnection();
        source.addTransceiver(dest.stream.getAudioTracks()[0], { direction: "sendonly" });
        source.addTransceiver(canvas.captureStream(5).getVideoTracks()[0], { direction: "sendonly" });
        source.addTransceiver(canvas.captureStream(5).getVideoTracks()[0], { direction: "sendonly" });
        const sink = rx.protectPeer(new RTCPeerConnection(rx.peerConfig()));
        const log = [];
        sink.ontrack = () => {
            // what a reconcile-all handler observes on this track event
            log.push(sink.getTransceivers().map(t => ({ mid: t.mid, gate: String(rx.authenticated(t.receiver)), enabled: t.receiver.track.enabled, transform: api === "script" ? !!t.receiver.transform : null })));
        };
        await source.setLocalDescription(await source.createOffer());
        await sink.setRemoteDescription(source.localDescription);
        return log;
    }, api);
    // Review 2a-fix2 P2-3 (repro adopted): during the first track event no
    // receiver - protected or not yet - reports authenticated.
    expect(seen.length).toBeGreaterThan(0);
    for (const event of seen) expect(event.every(r => r.gate === "false")).toBe(true);
});
