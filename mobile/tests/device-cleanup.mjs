// device-cleanup.mjs — accounts have a device limit and every linked phone
// (and every desktop session's fresh key) counts. Specs that link phones to
// the owner release them again so later specs can still link.
import { Msg } from "../src/core/frame.js";

export async function releaseDevices(desktop) {
    try {
        const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
        for (const device of list.devices || []) {
            if (device.current || device.first_device) continue;
            await desktop.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: device.device_id }, Msg.DeviceResult).catch(() => {});
        }
        if (!(list.devices || []).find(d => d.current)?.first_device) {
            await desktop.conn.request(Msg.DeviceRequest, { action: "sign_out" }, Msg.DeviceResult, { accept: r => r.action === "sign_out" }).catch(() => {});
        }
    } catch { /* best effort */ }
    desktop.conn.close();
}
