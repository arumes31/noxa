import assert from "node:assert/strict";
import { test } from "node:test";
import { describeUploadFailure, parseUploadFailure, UPLOAD_STAGES } from "../src/upload-stage.js";
import { setLanguage, t } from "../src/i18n.js";

test("native stage prefixes are parsed and unknown errors fall back to the caller's stage", () => {
    assert.deepEqual(parseUploadFailure("[upload:read] invalid file data"), { stage: "read", detail: "invalid file data" });
    assert.deepEqual(parseUploadFailure(new Error("[upload:encrypt] seal failed")), { stage: "encrypt", detail: "seal failed" });
    assert.deepEqual(parseUploadFailure("[upload:transfer] server refused", "publish"), { stage: "transfer", detail: "server refused" });
    assert.deepEqual(parseUploadFailure("not connected"), { stage: "transfer", detail: "not connected" });
    assert.deepEqual(parseUploadFailure("permission denied", "publish"), { stage: "publish", detail: "permission denied" });
    assert.deepEqual(parseUploadFailure("[upload:bogus] x", "publish"), { stage: "publish", detail: "[upload:bogus] x" });
    assert.deepEqual(parseUploadFailure(null), { stage: "transfer", detail: "" });
});

test("every stage has a distinct label and a concrete next step in both languages", () => {
    for (const language of ["en", "de"]) {
        setLanguage(language);
        const labels = new Set(), hints = new Set();
        for (const stage of UPLOAD_STAGES) {
            const described = describeUploadFailure(`[upload:${stage}] detail`, "transfer");
            assert.equal(described.stage, stage);
            assert.notEqual(described.label, "upload.stage." + stage, `${language} ${stage} label`);
            assert.notEqual(described.hint, "upload.stage." + stage + ".hint", `${language} ${stage} hint`);
            assert.equal(described.summary, described.label + " — detail");
            labels.add(described.label); hints.add(described.hint);
        }
        assert.equal(labels.size, UPLOAD_STAGES.length);
        assert.equal(hints.size, UPLOAD_STAGES.length);
    }
    setLanguage("en");
    assert.equal(describeUploadFailure("", "publish").summary, t("upload.stage.publish"));
    assert.match(describeUploadFailure("x", "publish").hint, /cleaned up automatically/);
});
