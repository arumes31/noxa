import assert from "node:assert/strict";
import test from "node:test";
import { withScreenStartupBitrateHint } from "../src/video-startup-bitrate.js";

const screen = [
    "m=video 9 UDP/TLS/RTP/SAVPF 102 103 96 97", "a=mid:screen", "a=recvonly",
    "a=ice-ufrag:secret", "a=ice-pwd:private-password", "a=fingerprint:sha-256 AA:BB", "a=setup:active",
    "a=rtpmap:102 H264/90000", "a=fmtp:102 packetization-mode=1;profile-level-id=42001f;level-asymmetry-allowed=1",
    "a=rtcp-fb:102 nack pli", "a=rtpmap:103 rtx/90000", "a=fmtp:103 apt=102",
    "a=rtpmap:96 VP8/90000", "a=rtpmap:97 rtx/90000", "a=fmtp:97 apt=96",
];
const prefix = ["v=0", "o=- 123 456 IN IP4 127.0.0.1", "s=-", "t=0 0", "a=group:BUNDLE voice screen camera",
    "m=audio 9 UDP/TLS/RTP/SAVPF 111", "a=mid:voice", "a=sendrecv", "a=rtpmap:111 opus/48000/2", "a=fmtp:111 useinbandfec=1"];
const suffix = ["m=video 9 UDP/TLS/RTP/SAVPF 102", "a=mid:camera", "a=sendrecv", "a=rtpmap:102 H264/90000", "a=fmtp:102 profile-level-id=42001f"];
const fixture = (parts = screen, eol = "\r\n") => [...prefix, ...parts, ...suffix].join(eol) + eol;
const options = { mid: "screen", payloadType: 102, bitrateKbps: 2000 };
const apply = (sdp, changes = {}, type = "answer") => withScreenStartupBitrateHint({ type, sdp }, { ...options, ...changes });
const unchanged = (sdp, changes, type) => assert.deepEqual(apply(sdp, changes, type), { sdp, changed: false });

test("adds only a start hint to the exact screen payload, preserving bundled media and security bytes", () => {
    const sdp = fixture();
    const old = screen[8];
    assert.deepEqual(apply(sdp), { sdp: sdp.replace(old, `${old};x-google-start-bitrate=2000`), changed: true });
    assert.equal(apply(sdp).sdp.includes("x-google-min-bitrate"), false);
    assert.equal(apply(sdp).sdp.includes("x-google-max-bitrate"), false);
    unchanged(apply(sdp).sdp);
});

test("preserves LF and mixed line endings and final newline state", () => {
    for (const sdp of [fixture(screen, "\n"), fixture().replace("a=mid:screen\r\n", "a=mid:screen\n"), fixture().slice(0, -2)]) {
        assert.equal(apply(sdp).sdp, sdp.replace(screen[8], `${screen[8]};x-google-start-bitrate=2000`));
    }
});

test("creates VP8 fmtp at the selected mapping and respects a smaller seed", () => {
    const sdp = fixture(screen, "\n");
    assert.deepEqual(apply(sdp, { payloadType: 96, bitrateKbps: 500 }), {
        sdp: sdp.replace("a=rtpmap:96 VP8/90000\n", "a=rtpmap:96 VP8/90000\na=fmtp:96 x-google-start-bitrate=500\n"), changed: true,
    });
    const final = "v=0\nm=video 9 UDP/TLS/RTP/SAVPF 96\na=mid:screen\na=recvonly\na=rtpmap:96 VP8/90000";
    assert.equal(apply(final, { payloadType: 96 }).sdp, final + "\na=fmtp:96 x-google-start-bitrate=2000");
});

test("supports HEVC without rewriting profile, level or transport parameters", () => {
    const sdp = fixture(screen.map(line => line.replace("H264/90000", "H265/90000").replace("packetization-mode=1;profile-level-id=42001f;level-asymmetry-allowed=1", "profile-id=1;tier-flag=0;level-id=123;tx-mode=SRST")));
    assert.equal(apply(sdp).sdp, sdp.replace("level-id=123;tx-mode=SRST", "level-id=123;tx-mode=SRST;x-google-start-bitrate=2000"));
});

test("preserves an unrelated bundled data channel and valid RED format list", () => {
    const withRed = screen.map(line => line.startsWith("m=video ") ? line + " 120" : line)
        .concat(["a=rtpmap:120 red/90000", "a=fmtp:120 102/96"]);
    const sdp = fixture(withRed) + "m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\na=mid:data\r\na=sctp-port:5000\r\n";
    assert.equal(apply(sdp).sdp, sdp.replace(screen[8], `${screen[8]};x-google-start-bitrate=2000`));
});

test("never edits offers, pranswers, rejected or non-sending directions", () => {
    for (const type of ["offer", "pranswer", "rollback", "ANSWER", undefined]) unchanged(fixture(), {}, type === undefined ? "" : type);
    for (const replacement of ["a=inactive", "a=sendonly", "a=recvonly extra"]) unchanged(fixture(screen.map(line => line === "a=recvonly" ? replacement : line)));
    unchanged(fixture(screen.map(line => line.replace("m=video 9 ", "m=video 0 "))));
    assert.equal(apply(fixture(screen.filter(line => line !== "a=recvonly"))).changed, true, "default direction is sendrecv");
    const inherited = fixture(screen.filter(line => line !== "a=recvonly")).replace("t=0 0\r\n", "t=0 0\r\na=sendonly\r\n");
    unchanged(inherited);
});

test("requires exact unambiguous MID and a primary 90kHz video payload", () => {
    for (const changes of [{ mid: "missing" }, { mid: "voice" }, { mid: "screen\r\na=sendrecv" }, { payloadType: 103 }, { payloadType: 97 }, { payloadType: 120 }, { payloadType: "102" }]) unchanged(fixture(), changes);
    unchanged(fixture().replace("a=mid:camera", "a=mid:screen"));
    unchanged(fixture().replace("a=mid:screen\r\n", "a=mid:screen\r\na=mid:screen\r\n"));
    for (const codec of ["AV1/90000", "red/90000", "ulpfec/90000", "flexfec-03/90000", "H264/8000", "H264/90000/2"]) unchanged(fixture().replace("a=rtpmap:102 H264/90000", `a=rtpmap:102 ${codec}`));
});

test("rejects malformed or duplicate media, payload and format declarations", () => {
    const mutations = [
        ["m=video 9 UDP/TLS/RTP/SAVPF 102 103 96 97", "m=video bad UDP/TLS/RTP/SAVPF 102"],
        ["m=video 9 UDP/TLS/RTP/SAVPF 102 103 96 97", "m=video 9 UDP/TLS/RTP/SAVPF 102 102"],
        ["m=video 9 UDP/TLS/RTP/SAVPF 102 103 96 97", "m=video 9 UDP/TLS/RTP/SAVPF 128"],
        ["a=rtpmap:102 H264/90000", "a=rtpmap:102 H264/90000\r\na=rtpmap:102 VP8/90000"],
        ["a=rtpmap:102 H264/90000", "a=rtpmap:102 H264"],
        ["a=rtpmap:102 H264/90000", "a=rtpmap:120 H264/90000"],
        [screen[8], screen[8] + "\r\n" + screen[8]],
        [screen[8], "a=fmtp:102"],
        [screen[8], "a=fmtp:102 profile-level-id"],
        [screen[8], "a=fmtp:102 profile-level-id=42001f;PROFILE-LEVEL-ID=42001f"],
        [screen[8], "a=fmtp:102 profile-level-id="],
        ["a=fmtp:103 apt=102", "a=fmtp:103 apt="],
        ["a=recvonly", "a=recvonly\r\na=sendrecv"],
        ["a=mid:screen", "a=mid:screen "],
        ["v=0", "v=1"],
        ["a=ice-ufrag:secret", "a=ice-ufrag:secret\rbroken"],
    ];
    for (const [from, to] of mutations) unchanged(fixture().replace(from, to));
});

test("does not replace pre-existing hints, even on another payload or with different case", () => {
    for (const addition of [";x-google-start-bitrate=750", ";X-GOOGLE-START-BITRATE = 900", ";x-google-start-bitrate"]) unchanged(fixture().replace(screen[8], screen[8] + addition));
    unchanged(fixture().replace("a=fmtp:103 apt=102", "a=fmtp:103 apt=102;x-google-start-bitrate=500"));
});

test("enforces finite integer seed bounds without clamping unsafe values", () => {
    for (const bitrateKbps of [0, -1, 2001, 1.5, NaN, Infinity, "2000", null]) unchanged(fixture(), { bitrateKbps });
    assert.equal(apply(fixture(), { bitrateKbps: 1 }).changed, true);
    assert.deepEqual(withScreenStartupBitrateHint(null), { sdp: undefined, changed: false });
    assert.deepEqual(withScreenStartupBitrateHint({ type: "answer", sdp: fixture() }, null), { sdp: fixture(), changed: false });
});
