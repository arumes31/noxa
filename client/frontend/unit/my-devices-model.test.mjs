import test from "node:test";
import assert from "node:assert/strict";
import { defaultDeviceName, deviceSessionKind, mergeDevicePresence, renameDeviceLocally, validDeviceName } from "../src/my-devices-model.js";

const translate = (key, params = {}) => `${key}${params.platform ? `(${params.platform})` : ""}`;

const devices = [
    { device_id: "desk", kind: "desktop", name: "Office PC", current: true, last_seen_at: 1000 },
    { device_id: "phone-new", kind: "web", name: "Phone", last_seen_at: 900 },
    { device_id: "phone-old", kind: "web", name: "", last_seen_at: 100 },
    { device_id: "laptop", kind: "desktop", name: "Laptop", last_seen_at: 500 },
    { device_id: "gone", kind: "web", name: "Old", last_seen_at: 50, revoked_at: 60 },
];

test("deviceSessionKind maps on_mobile sessions to web devices", () => {
    assert.equal(deviceSessionKind({ on_mobile: true }), "web");
    assert.equal(deviceSessionKind({ on_mobile: false }), "desktop");
    assert.equal(deviceSessionKind(undefined), "desktop");
});

test("defaultDeviceName keeps real names and fills the server's placeholder with Mobile · platform", () => {
    assert.equal(defaultDeviceName({ name: "Daniel's iPhone", kind: "web" }, translate), "Daniel's iPhone");
    assert.equal(defaultDeviceName({ name: "Phone", kind: "web" }, translate), "devices.defaultMobileName(devices.kind.web)");
    assert.equal(defaultDeviceName({ name: "", kind: "web" }, translate), "devices.defaultMobileName(devices.kind.web)");
    assert.equal(defaultDeviceName({ name: "  ", kind: "desktop" }, translate), "devices.defaultDesktopName(devices.kind.desktop)");
    assert.equal(defaultDeviceName(null, translate), "devices.defaultDesktopName(devices.kind.desktop)");
});

test("mergeDevicePresence marks this device online and drops revoked devices", () => {
    const rows = mergeDevicePresence(devices, [{ client_id: "me", unique_id: "u1" }], { myUniqueID: "u1", myClientID: "me" });
    assert.deepEqual(rows.map(row => row.deviceID), ["desk", "phone-new", "laptop", "phone-old"]);
    const desk = rows[0];
    assert.equal(desk.current, true);
    assert.equal(desk.online, true);
    assert.ok(rows.slice(1).every(row => !row.online), "no other session, no other device online");
});

test("mergeDevicePresence derives online devices from the account's live sessions per kind", () => {
    const sessions = [
        { client_id: "me", unique_id: "u1", on_mobile: false },
        { client_id: "s-phone", unique_id: "u1", on_mobile: true },
        { client_id: "stranger", unique_id: "u2", on_mobile: true },
        { client_id: "stranger-2", unique_id: "u2", on_mobile: false },
    ];
    const rows = mergeDevicePresence(devices, sessions, { myUniqueID: "u1", myClientID: "me" });
    const byID = Object.fromEntries(rows.map(row => [row.deviceID, row]));
    assert.equal(byID["phone-new"].online, true, "the most recently used phone takes the live mobile session");
    assert.equal(byID["phone-old"].online, false);
    assert.equal(byID.laptop.online, false, "another account's desktop session is not ours");
    assert.deepEqual(rows.map(row => row.deviceID), ["desk", "phone-new", "laptop", "phone-old"], "this device, then online, then by last use");
});

test("mergeDevicePresence never counts this session twice and tolerates missing data", () => {
    const rows = mergeDevicePresence(devices, [{ client_id: "me", unique_id: "u1", on_mobile: false }, { client_id: "me", unique_id: "u1" }], { myUniqueID: "u1", myClientID: "me" });
    assert.equal(rows.find(row => row.deviceID === "laptop").online, false);
    assert.deepEqual(mergeDevicePresence(null, null), []);
    assert.deepEqual(mergeDevicePresence([{ kind: "web" }, null], undefined), []);
    const [row] = mergeDevicePresence([{ device_id: "x", kind: "unknown", needs_confirm: true }], []);
    assert.equal(row.kind, "desktop");
    assert.equal(row.needsConfirm, true);
    assert.equal(row.lastSeenAt, 0);
});

test("renameDeviceLocally changes one device without touching the others", () => {
    const renamed = renameDeviceLocally(devices, "phone-new", "Pixel");
    assert.equal(renamed.find(device => device.device_id === "phone-new").name, "Pixel");
    assert.equal(renamed.find(device => device.device_id === "desk").name, "Office PC");
    assert.equal(devices.find(device => device.device_id === "phone-new").name, "Phone", "input untouched");
    assert.deepEqual(renameDeviceLocally(null, "x", "y"), []);
});

test("validDeviceName trims, collapses whitespace and bounds the length", () => {
    assert.equal(validDeviceName("  Daniel's   Pixel "), "Daniel's Pixel");
    assert.equal(validDeviceName(""), "");
    assert.equal(validDeviceName("   "), "");
    assert.equal(validDeviceName(null), "");
    assert.equal(validDeviceName("a".repeat(64)), "a".repeat(64));
    assert.equal(validDeviceName("a".repeat(65)), "");
});
