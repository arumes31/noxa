import assert from "node:assert/strict";
import { createRequire } from "node:module";

const require = createRequire(new URL("../../../client/frontend/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const endpoint = process.argv[2];
const browser = await chromium.launch({ headless: true });
try {
    const page = await browser.newPage();
    await page.exposeFunction("request", async (path, sdp = "") => {
        const response = await fetch(`${endpoint}/${path}`, {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ sdp }),
        });
        if (!response.ok) throw new Error(await response.text());
        const result = await response.json();
        return ["parameters", "received"].includes(path) ? result : result.sdp;
    });
    const result = await page.evaluate(async () => {
        const pc = new RTCPeerConnection();
        pc.ontrack = event => {
            const video = document.createElement("video"); video.muted = true; video.autoplay = true;
            video.srcObject = new MediaStream([event.track]); document.body.append(video);
        };
        const canvas = document.createElement("canvas");
        canvas.width = 320;
        canvas.height = 180;
        const stream = canvas.captureStream(20);
        let frame = 0;
        const timer = setInterval(() => {
            const ctx = canvas.getContext("2d");
            ctx.fillStyle = frame++ % 2 ? "red" : "blue";
            ctx.fillRect(0, 0, canvas.width, canvas.height);
        }, 50);
        const extmaps = sdp => sdp.split(/\r?\n/).filter(line => line.startsWith("a=extmap:"));
        try {
            pc.addTransceiver("audio");
            let offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            if (pc.iceGatheringState !== "complete") await new Promise(resolve => {
                const listener = () => {
                    if (pc.iceGatheringState === "complete") {
                        pc.removeEventListener("icegatheringstatechange", listener);
                        resolve();
                    }
                };
                pc.addEventListener("icegatheringstatechange", listener);
            });
            await pc.setRemoteDescription({ type: "answer", sdp: await window.request("initial", pc.localDescription.sdp) });

            // The server offer crosses Chromium's independent first-video offer.
            // Rollback retains Chromium's allocated extmap IDs on the new MID.
            const crossing = await window.request("crossing");
            pc.addTrack(stream.getVideoTracks()[0], stream);
            offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            const localExtensions = extmaps(offer.sdp);
            const remoteExtensions = extmaps(crossing);
            const originalExtensions = extmaps(await window.request("local-offer"));
            const idOf = line => Number(line.match(/^a=extmap:(\d+)/)?.[1]);
            const uriOf = line => line.split(" ")[1];
            const collides = remote => localExtensions.some(local => remote.some(other => idOf(local) === idOf(other) && uriOf(local) !== uriOf(other)));
            if (!collides(originalExtensions) || collides(remoteExtensions)) throw new Error(`first-video fixture must exercise and avoid the original extmap collision: ${JSON.stringify({localExtensions,originalExtensions,remoteExtensions})}`);
            await pc.setLocalDescription({ type: "rollback" });
            await pc.setRemoteDescription({ type: "offer", sdp: crossing });
            const answer = await pc.createAnswer();
            await pc.setLocalDescription(answer);
            await window.request("answer", answer.sdp);

            // Preserve legacy simulcast, including actual receiver RID lookup.
            pc.addTransceiver(stream.getVideoTracks()[0], {
                direction: "sendonly", sendEncodings: [{ rid: "h", scaleResolutionDownBy: 2 }, { rid: "f" }],
            });
            offer = await pc.createOffer();
            await pc.setLocalDescription(offer);
            await pc.setRemoteDescription({ type: "answer", sdp: await window.request("offer", offer.sdp) });
            const binding = await window.request("parameters");
            const parameters = binding.receivers;
            const negotiated = uri => parameters.find(item => item.URI === uri)?.ID;
            const offered = uri => Number(extmaps(offer.sdp).find(line => line.split(" ")[1] === uri)?.match(/^a=extmap:(\d+)/)?.[1]);
            for (const uri of ["urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id", "urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id"]) {
                if (!(offered(uri) >= 16) || negotiated(uri) !== offered(uri)) {
                    throw new Error(`wrong negotiated receiver IDs: ${JSON.stringify(parameters)}`);
                }
                const senderID = binding.outgoing.extensions.find(item => item.URI === uri)?.ID;
                if (senderID !== offered(uri)) throw new Error(`sender binding differs from wire answer: ${JSON.stringify(binding.outgoing)}`);
            }
            // Once accepted, server reoffers retain the very same mappings.
            await pc.setRemoteDescription({ type: "offer", sdp: await window.request("reoffer") });
            const reanswer = await pc.createAnswer();
            await pc.setLocalDescription(reanswer);
            await window.request("answer", reanswer.sdp);
            const decodedFrames = async () => [...(await pc.getStats()).values()].filter(row => row.type === "inbound-rtp" && row.kind === "video").reduce((sum, row) => sum + (row.framesDecoded || 0), 0);
            const baselineDecoded = await decodedFrames();
            const baselineSent = (await window.request("parameters")).outgoing.sent;
            const received = await window.request("received");
            let decoded = 0, outgoing;
            for (let i = 0; i < 100; i++) {
                decoded = await decodedFrames();
                outgoing = (await window.request("parameters")).outgoing;
                if (decoded > baselineDecoded && outgoing.sent > baselineSent) break;
                await new Promise(resolve => setTimeout(resolve, 50));
            }
            if (!(decoded > baselineDecoded) || !(outgoing.sent > baselineSent) || outgoing.boundRIDID !== offered("urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id")) throw new Error(`outbound RTP not decoded with negotiated sender IDs: ${JSON.stringify({decoded,baselineDecoded,outgoing,baselineSent,inbound:[...(await pc.getStats()).values()].filter(row=>row.type==="inbound-rtp"),remote:pc.remoteDescription.sdp.split(/\r?\n/).filter(line=>/^a=(?:ssrc|msid|mid|rid|simulcast)/.test(line))})}`);
            return { received, decoded, baselineDecoded, baselineSent, outgoing, state: pc.signalingState, track: stream.getVideoTracks()[0].readyState, parameters, localExtensions, remoteExtensions };
        } finally {
            clearInterval(timer);
            pc.close();
            stream.getTracks().forEach(track => track.stop());
        }
    });
    assert.equal(result.state, "stable");
    assert.equal(result.track, "live");
    assert.ok(result.received.f || result.received.h, "RID-bearing simulcast RTP received");
    console.log(JSON.stringify(result));
} finally {
    await browser.close();
}
