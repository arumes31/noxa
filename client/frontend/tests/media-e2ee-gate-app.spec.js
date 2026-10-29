import { expect, test } from "./fixtures.js";

// Adopted from review 2a-fix3 (R4): drive the REAL remote-media reconcile (desktop onTrack path)
// with the real gate on a three-receiver offer. Nothing may be attached or
// enabled before an authenticated frame (no keys are ever provided here).
for (const api of ["script", "streams"]) test(`multi-receiver offer attaches nothing unauthenticated (${api})`, async ({ page }) => {
    await page.route("**/__gate__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>gate</title>" }));
    await page.goto("/__gate__");
    const out = await page.evaluate(async api => {
        const { MediaE2EE } = await import("/src/media-e2ee.js");
        const { startRemoteMedia } = await import("/src/remote-media.js");
        const rx = new MediaE2EE({ api });
        await rx.ready;
        const canvas = document.createElement("canvas"); canvas.width = 160; canvas.height = 90; canvas.getContext("2d").fillRect(0, 0, 160, 90);
        const ac = new AudioContext(); const dest = ac.createMediaStreamDestination(); ac.createOscillator().connect(dest);
        const source = new RTCPeerConnection();
        const streams = ["c1|mic", "c2|cam", "c3|screen"].map(id => new MediaStream());
        source.addTransceiver(dest.stream.getAudioTracks()[0], { direction: "sendonly", streams: [streams[0]] });
        source.addTransceiver(canvas.captureStream(5).getVideoTracks()[0], { direction: "sendonly", streams: [streams[1]] });
        source.addTransceiver(canvas.captureStream(5).getVideoTracks()[0], { direction: "sendonly", streams: [streams[2]] });
        const sink = rx.protectPeer(new RTCPeerConnection(rx.peerConfig()));
        const attached = []; let waiters = 0;
        startRemoteMedia(sink, () => true, (track, id) => {
            if (!rx.authenticated(track)) { waiters++; rx.onAuthenticated(track, () => {}); return false; }
            attached.push({ id, enabled: track.enabled }); track.enabled = true;
        }, () => {}, (track) => rx.authenticated(track));
        await source.setLocalDescription(await source.createOffer());
        await sink.setRemoteDescription(source.localDescription);
        await sink.setLocalDescription(await sink.createAnswer());
        await source.setRemoteDescription(sink.localDescription);
        await new Promise(r => setTimeout(r, 300));
        return { attached, waiters, tracks: sink.getTransceivers().map(t => ({ mid: t.mid, enabled: t.receiver.track.enabled, auth: rx.authenticated(t.receiver) })) };
    }, api);
    expect(out.attached).toEqual([]);
    expect(out.tracks.every(t => !t.enabled && !t.auth)).toBe(true);
});
