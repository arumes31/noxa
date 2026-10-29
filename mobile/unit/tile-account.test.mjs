import test from "node:test";
import assert from "node:assert/strict";
import { accountBlocks, tileAccount } from "../src/ui/media-e2ee-text.js";

// Review 2b P3-3: the visible tile label names the key source with its caveat
// and shows the account in 4-character blocks (24 characters), the full
// account in the explanation.
test("tile account label names the key source, with the caveat, in fingerprint blocks", () => {
    const account = "AbCdEfGhIjKlMnOpQrStUvWxYz012345";
    assert.equal(accountBlocks(account), "AbCd EfGh IjKl MnOp QrSt UvWx …");
    assert.equal(accountBlocks(account, Infinity), "AbCd EfGh IjKl MnOp QrSt UvWx Yz01 2345");
    const label = tileAccount(account);
    assert.match(label.label, /^keys from AbCd EfGh IjKl MnOp QrSt UvWx … · not proof of who is speaking$/);
    assert.match(label.title, /Yz01 2345/);
    assert.equal(tileAccount(""), null);
});
