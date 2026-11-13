import test from "node:test";
import assert from "node:assert/strict";
import { inertOthers } from "../src/ui/dom.js";

// An open modal makes the rest of the page inert; closing restores only what
// it changed, so nested dialogs and already-inert nodes unwind correctly.

test("inertOthers contains nested dialogs and restores the page state", t => {
    const node = (live = null, inert = false) => ({ inert, matches: () => live !== null && live !== "off" });
    const app = node("off"), toasts = node("polite"), hidden = node(null, true);
    const body = { children: [app, toasts, hidden] };
    const previous = Object.getOwnPropertyDescriptor(globalThis, "document");
    Object.defineProperty(globalThis, "document", { configurable: true, value: { body } });
    t.after(() => {
        if (previous) Object.defineProperty(globalThis, "document", previous);
        else delete globalThis.document;
    });

    const outer = node();
    body.children.push(outer);
    const closeOuter = inertOthers(outer);
    assert.deepEqual([app.inert, toasts.inert, hidden.inert, outer.inert], [true, false, true, false]);

    const inner = node();
    body.children.push(inner);
    const closeInner = inertOthers(inner);
    assert.equal(outer.inert, true, "the outer dialog is inert behind the inner one");
    assert.equal(inner.inert, false);

    body.children.pop();
    closeInner();
    assert.deepEqual([app.inert, toasts.inert, hidden.inert, outer.inert], [true, false, true, false], "the outer dialog is live again, the page is not");

    body.children.pop();
    closeOuter();
    assert.deepEqual([app.inert, toasts.inert, hidden.inert], [false, false, true], "a node that was already inert stays inert");
});
