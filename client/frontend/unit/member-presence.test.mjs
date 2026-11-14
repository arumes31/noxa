import assert from "node:assert/strict";
import { test } from "node:test";
import { accountKey, deviceKind, groupSessionsByAccount, onlineMemberCount, visibleInChannelTree } from "../src/member-presence.js";
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
test("sessions of one account fold into a single row led by the viewer, then the desktop session", () => {
 const desktop = {client_id:"d",unique_id:"u1"}, phone = {client_id:"p",unique_id:"u1",on_mobile:true}, guest = {client_id:"g"};
 const groups = groupSessionsByAccount([phone, desktop, guest]);
 assert.deepEqual(groups.map(g => [g.primary.client_id, g.others.map(s => s.client_id)]), [["d", ["p"]], ["g", []]]);
 assert.equal(groupSessionsByAccount([desktop, phone], "p")[0].primary.client_id, "p");
 assert.equal(groupSessionsByAccount([phone, {client_id:"d2",unique_id:"u1",is_speaking:true}, desktop])[0].primary.client_id, "d2");
 assert.deepEqual(groupSessionsByAccount([{client_id:"a"},{client_id:"b"}]).map(g => g.sessions.length), [1, 1]);
 assert.equal(accountKey(phone), accountKey(desktop));
 assert.equal(deviceKind(phone), "mobile"); assert.equal(deviceKind(desktop), "desktop");
});

test('the connecting wave follows the server marks and stops for reduced motion', async () => {
    const { memberConnecting } = await import('../src/member-presence.js');
    assert.equal(memberConnecting({ connecting: true }), true, 'joining or resuming until the peer connects');
    assert.equal(memberConnecting({ reconnecting: true }), true);
    assert.equal(memberConnecting({ updating: true }), true);
    assert.equal(memberConnecting({ channel_id: 3 }), false, 'a connected member has no wave');
    assert.equal(memberConnecting(null), false);
    const { readFile } = await import('node:fs/promises');
    const css = await readFile(new URL('../src/style.css', import.meta.url), 'utf8');
    const reduced = css.slice(css.lastIndexOf('@media (prefers-reduced-motion: reduce)'));
    assert.match(css, /\.member-connecting \{[^}]*animation: member-connecting-wave/);
    assert.match(reduced, /\.member-connecting \{[^}]*animation: none[^}]*opacity/, 'reduced motion shows a static dimmed name');
});
