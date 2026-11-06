import assert from "node:assert/strict";
import test from "node:test";
import { presenceState, withPresenceDot } from "../src/presence-dot.js";

function fakeDocument() {
    const make = tag => ({ tagName: tag, className: "", dataset: {}, attrs: {}, children: [],
        setAttribute(name, value) { this.attrs[name] = value; }, append(...nodes) { this.children.push(...nodes); } });
    return { createElement: make };
}

test("presence states map to four distinct dots; invisible looks offline", () => {
    assert.equal(presenceState(""), "online");
    assert.equal(presenceState(undefined), "online");
    assert.equal(presenceState("away"), "away");
    assert.equal(presenceState("busy"), "busy");
    assert.equal(presenceState("invisible"), "offline");
    assert.equal(presenceState("offline"), "offline");
    assert.equal(presenceState("<script>"), "online");
});

test("the dot wraps the avatar and is hidden from assistive technology", () => {
    const avatar = { tagName: "span" };
    const wrap = withPresenceDot(avatar, "busy", fakeDocument());
    assert.equal(wrap.className, "presence-wrap");
    assert.equal(wrap.children[0], avatar);
    const dot = wrap.children[1];
    assert.equal(dot.className, "presence-dot");
    assert.equal(dot.dataset.presence, "busy");
    assert.equal(dot.attrs["aria-hidden"], "true");
});
