// npm audit gate: fails on high/critical advisories except the ones listed
// below. npm audit has no ignore option, so the exceptions live here.
// Usage: npm audit --json | node scripts/npm-audit-gate.mjs
import { readFileSync } from "node:fs";

// Each exception needs a reason and should be removed once a fix ships.
const ACCEPTED = new Map([
    // picomatch <= 4.0.7 ReDoS through attacker-supplied extglobs. Reached only
    // via build tooling (vite -> tinyglobby/fdir) with globs from our own
    // config, never user input. No patched release exists yet (2026-11-17).
    ["GHSA-fc5q-39xc-vjpf", "build-time only, globs are not attacker-controlled"],
]);
const BLOCKING = new Set(["high", "critical"]);

const report = JSON.parse(readFileSync(0, "utf8"));
if (report.error) {
    console.error(`::error::npm audit failed: ${JSON.stringify(report.error)}`);
    process.exit(1);
}
const failing = new Map();
const accepted = new Set();
for (const vuln of Object.values(report.vulnerabilities ?? {})) {
    for (const via of vuln.via ?? []) {
        if (typeof via !== "object" || !BLOCKING.has(via.severity)) continue;
        const id = String(via.url ?? "").split("/").pop();
        if (ACCEPTED.has(id)) accepted.add(id);
        else failing.set(id || via.title, `${via.name}: ${via.title} (${via.severity})`);
    }
}
for (const id of accepted) console.log(`accepted ${id}: ${ACCEPTED.get(id)}`);
for (const [id, text] of failing) console.error(`::error::${id} ${text}`);
process.exit(failing.size ? 1 : 0);
