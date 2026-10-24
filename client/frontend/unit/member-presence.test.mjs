import assert from "node:assert/strict";
import { test } from "node:test";
import { onlineMemberCount, visibleInChannelTree } from "../src/member-presence.js";
test("online summary counts accounts while preserving distinct guests", () => {
 assert.equal(onlineMemberCount([{user_id:7,client_id:"desktop"},{user_id:7,client_id:"phone",on_mobile:true},{unique_id:"guest-a"},{unique_id:"guest-b"}]),3);
 assert.equal(onlineMemberCount([{unique_id:"same"},{unique_id:"same"}]),1);
 assert.equal(onlineMemberCount([]),0);
});
test("mobile presence only appears in the channel tree after joining voice", () => {
 assert.equal(visibleInChannelTree({on_mobile:true,channel_id:0}),false);
 assert.equal(visibleInChannelTree({on_mobile:true,channel_id:15}),true);
 assert.equal(visibleInChannelTree({channel_id:0}),true);
});
