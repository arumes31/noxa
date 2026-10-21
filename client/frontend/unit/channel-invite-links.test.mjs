import test from "node:test";
import assert from "node:assert/strict";
import { buildChannelInvite, parseChannelInvite } from "../src/channel-invite-links.js";
const token = "A".repeat(43);
test("invitation roundtrip preserves exact server and channel", () => {
    assert.deepEqual(parseChannelInvite(buildChannelInvite("[::1]:9987", 9, token)), { address: "[::1]:9987", channelID: 9, token });
});
test("invitation rejects credentials, foreign schemes, malformed scopes and secrets", () => {
    for (const link of ["https://host/invite/1#" + token, "noxa://user@host/invite/1#" + token, "noxa://host/invite/-1#" + token, "noxa://host/invite/9007199254740992#" + token, "noxa://host/invite/1?token=" + token, "noxa://host/invite/1#short"]) assert.equal(parseChannelInvite(link), null);
    for (const host of ["https://host", "host/path", "user@host", "host?secret=1"]) assert.equal(buildChannelInvite(host, 1, token), null);
});
