import test from "node:test";
import assert from "node:assert/strict";
import { buildChannelInvite, handleDeepLink, parseChannelInvite } from "../src/channel-invite-links.js";
const token = "A".repeat(43);
test("invitation roundtrip preserves exact server and channel", () => {
    assert.deepEqual(parseChannelInvite(buildChannelInvite("[::1]:9987", 9, token)), { address: "[::1]:9987", channelID: 9, token });
});
test("invitation rejects credentials, foreign schemes, malformed scopes and secrets", () => {
    for (const link of ["https://host/invite/1#" + token, "noxa://user@host/invite/1#" + token, "noxa://host/invite/-1#" + token, "noxa://host/invite/9007199254740992#" + token, "noxa://host/invite/1?token=" + token, "noxa://host/invite/1#short"]) assert.equal(parseChannelInvite(link), null);
    for (const host of ["https://host", "host/path", "user@host", "host?secret=1"]) assert.equal(buildChannelInvite(host, 1, token), null);
});
test("deep links join only valid invites and only after confirmation", async () => {
    const link = "noxa://example.com/invite/12#" + token;
    const calls = [];
    const deps = answer => ({ confirm: async invite => { calls.push(["confirm", invite]); return answer; }, join: value => calls.push(["join", value]) });
    for (const bad of ["", "https://example.com/invite/12#" + token, "noxa://example.com/invite/12#short", "noxa://example.com/kick/12#" + token, 42]) assert.equal(await handleDeepLink(bad, deps(true)), false);
    assert.deepEqual(calls, []);
    assert.equal(await handleDeepLink(link, deps(false)), false);
    assert.deepEqual(calls, [["confirm", { address: "example.com", channelID: 12, token }]]);
    calls.length = 0;
    assert.equal(await handleDeepLink(link, deps(true)), true);
    assert.deepEqual(calls.map(call => call[0]), ["confirm", "join"]);
    assert.equal(calls[1][1], link);
});
