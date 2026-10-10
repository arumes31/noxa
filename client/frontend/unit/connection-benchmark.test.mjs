import test from "node:test";
import assert from "node:assert/strict";
import { benchmarkMessages, benchmarkResultRows, benchmarkSummary } from "../src/connection-benchmark-result.js";

test("no-return timing remains unknown, not a healthy zero", () => {
    const rows = benchmarkResultRows({ sent: 1000, returned: 0, unreturned: 1000 });
    assert.equal(rows[0][1], "1000 / 0");
    assert.equal(rows[2][1], "—");
    assert.equal(rows[3][1], "—");
});

test("result formatting rejects nonfinite timing and unexpected route strings", () => {
    const rows = benchmarkResultRows({ round_trip: { p50_ms: 0, p95_ms: Infinity, max_ms: 20 }, protocol: "secret", candidate_type: "address" });
    assert.equal(rows[2][1], "—");
    assert.equal(rows[5][1], "— / —");
});

test("both languages explain scope and delayed tail without claiming acoustic quality", () => {
    assert.match(benchmarkMessages.en.help, /microphone and speakers are not used/);
    assert.match(benchmarkMessages.de.help, /Mikrofon und Lautsprecher werden nicht verwendet/);
    assert.match(benchmarkSummary({ sent: 10 }).interpretation, /not a definitive network-loss/);
    assert.equal(benchmarkResultRows({}, "de")[0][0], "Pakete gesendet / zurückgekehrt");
});
