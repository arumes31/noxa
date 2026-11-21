import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { extractPresentedFingerprint, installDropGuard } from "../src/security.js";

const fingerprint = Array.from({ length: 32 }, (_, index) => index.toString(16).padStart(2, "0")).join(":");

describe("certificate mismatch details", () => {
    it("extracts and normalizes the presented SHA-256 fingerprint", () => {
        const detail = `tls fingerprint mismatch (presented: ${fingerprint.toUpperCase()})`;
        assert.equal(extractPresentedFingerprint(detail), fingerprint);
    });

    it("fails closed for missing, truncated, or malformed fingerprints", () => {
        assert.equal(extractPresentedFingerprint("tls fingerprint mismatch"), "");
        assert.equal(extractPresentedFingerprint("presented: aa:bb"), "");
        assert.equal(extractPresentedFingerprint(`presented: ${"gg:".repeat(31)}gg`), "");
        assert.equal(extractPresentedFingerprint(null), "");
    });
});

describe("window drop guard", () => {
    const listeners = {};
    installDropGuard({ addEventListener: (type, listener, options) => { assert.equal(options, undefined, "bubble phase"); listeners[type] = listener; } });
    const event = (defaultPrevented, dataTransfer) => ({ defaultPrevented, dataTransfer, preventDefault() { this.defaultPrevented = true; }, stopPropagation() { assert.fail("must not stop propagation"); } });

    it("refuses a drag no drop zone accepted", () => {
        const outside = event(false, { dropEffect: "copy" });
        listeners.dragover(outside);
        assert.equal(outside.defaultPrevented, true);
        assert.equal(outside.dataTransfer.dropEffect, "none");
        const bare = event(false);
        listeners.dragover(bare);
        assert.equal(bare.defaultPrevented, true);
    });

    it("leaves a drag accepted by a drop zone as it is", () => {
        const accepted = event(true, { dropEffect: "move" });
        listeners.dragover(accepted);
        assert.equal(accepted.dataTransfer.dropEffect, "move");
    });

    it("cancels the default action of a drop", () => {
        const dropped = event(false, {});
        listeners.drop(dropped);
        assert.equal(dropped.defaultPrevented, true);
    });

    it("leaves drags and drops on a file chooser to the chooser", () => {
        for (const type of ["dragover", "drop"]) {
            const onChooser = { ...event(false, { dropEffect: "copy" }), target: { type: "file" } };
            listeners[type](onChooser);
            assert.equal(onChooser.defaultPrevented, false);
            assert.equal(onChooser.dataTransfer.dropEffect, "copy");
        }
    });

    it("lets a text field take dragged text but not a file", () => {
        for (const type of ["dragover", "drop"]) {
            const text = { ...event(false, { dropEffect: "copy", types: ["text/plain"] }), target: { tagName: "TEXTAREA" } };
            listeners[type](text);
            assert.equal(text.defaultPrevented, false);
            const file = { ...event(false, { dropEffect: "copy", types: ["Files"] }), target: { tagName: "TEXTAREA" } };
            listeners[type](file);
            assert.equal(file.defaultPrevented, true);
        }
    });
});
