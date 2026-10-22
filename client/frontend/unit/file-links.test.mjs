import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { buildFileLink } from "../src/file-links.js";

const response = {
    health_port: 12337,
    scheme: "https",
    path: "/dl/0123456789abcdef0123456789abcdef",
};

describe("buildFileLink", () => {
    it("normalizes DNS, IPv4, IPv6, full control URLs, and explicitly secure server schemes", () => {
        assert.equal(buildFileLink("voice.example:12333", response),
            "https://voice.example:12337/dl/0123456789abcdef0123456789abcdef");
        assert.equal(buildFileLink("127.0.0.1:12333", { ...response, scheme: "HTTPS" }),
            "https://127.0.0.1:12337/dl/0123456789abcdef0123456789abcdef");
        assert.equal(buildFileLink("[2001:db8::7]:12333", response),
            "https://[2001:db8::7]:12337/dl/0123456789abcdef0123456789abcdef");
        assert.equal(buildFileLink("tls://Voice.Example:12333", response),
            "https://voice.example:12337/dl/0123456789abcdef0123456789abcdef");
    });

    it("uses only an explicit HTTPS public origin for proxy URLs", () => {
        assert.equal(buildFileLink("voice.example:12333", { ...response, public_origin: "https://files.example:8443/" }),
            "https://files.example:8443/dl/0123456789abcdef0123456789abcdef");
        for (const public_origin of ["http://files.example", "https://user:secret@files.example", "https://files.example/path", "https://files.example?", "https://files.example#", "https://files.example/\\evil", " https://files.example", null, 1]) {
            assert.equal(buildFileLink("voice.example:12333", { ...response, public_origin }), null);
        }
        for (const scheme of [undefined, null, "", "http"]) {
            assert.equal(buildFileLink("voice.example:12333", { ...response, scheme }), null);
        }
    });

    it("fails closed for malformed addresses and hostile protocol fields", () => {
        const invalidAddresses = ["", "2001:db8::7", "https://user@voice.example", "voice.example/path", "voice.example?x=1"];
        for (const address of invalidAddresses) {
            assert.equal(buildFileLink(address, response), null, address);
        }
        for (const invalid of [
            { ...response, scheme: "javascript" },
            { ...response, scheme: "https://evil.example" },
            { ...response, health_port: "12337" },
            { ...response, health_port: 0 },
            { ...response, path: "//evil.example" },
            { ...response, path: "/dl/0123456789abcdef0123456789abcdeg" },
            { ...response, path: "/dl/0123456789abcdef0123456789abcdef?x=1" },
        ]) {
            assert.equal(buildFileLink("voice.example:12333", invalid), null, JSON.stringify(invalid));
        }
    });
});
