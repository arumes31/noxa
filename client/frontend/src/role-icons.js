import { icon } from "./icons.js";
import { createSafeImage } from "./safe-media.js";

export const ROLE_ICON_SOURCE_MAX = 1024 * 1024;
export const ROLE_ICON_IMAGE_MAX = 8192;
export const ROLE_ICON_TOTAL_MAX = 128 * 1024;

export function roleIconElement(role) {
    const element = document.createElement("span");
    element.className = "role-icon";
    element.setAttribute("aria-hidden", "true");
    const data = role.icon_image;
    const image = typeof data === "string" && data.length <= 10946 && data.startsWith("data:image/png;base64,")
        ? createSafeImage(data) : null;
    if (image) element.append(image);
    else if (role.icon) element.textContent = role.icon;
    else {
        const name = String(role.name || "").toLowerCase();
        const asset = { administrator: "roleAdmin", admin: "roleAdmin", moderator: "shield", member: "roleMember", mitglied: "roleMember" }[name];
        if (asset) element.innerHTML = icon(asset);
        else element.textContent = "";
    }
    return element;
}

export async function prepareRoleIcon(file) {
    if (!file || file.size > ROLE_ICON_SOURCE_MAX || file.size < 33) throw new Error("invalid role image");
    const data = await file.arrayBuffer();
    const view = new DataView(data);
    // Inspect PNG dimensions before asking the browser to decode pixel data.
    if (view.getUint32(0) !== 0x89504e47 || view.getUint32(4) !== 0x0d0a1a0a ||
        view.getUint32(8) !== 13 || view.getUint32(12) !== 0x49484452) throw new Error("PNG required");
    const width = view.getUint32(16), height = view.getUint32(20);
    if (!width || !height || width > 2048 || height > 2048) throw new Error("invalid image dimensions");
    const bitmap = await createImageBitmap(new Blob([data], { type: "image/png" }));
    try {
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 32;
        const scale = Math.min(32 / bitmap.width, 32 / bitmap.height);
        const w = bitmap.width * scale, h = bitmap.height * scale;
        canvas.getContext("2d").drawImage(bitmap, (32 - w) / 2, (32 - h) / 2, w, h);
        const result = canvas.toDataURL("image/png");
        if (atob(result.split(",")[1]).length > ROLE_ICON_IMAGE_MAX) throw new Error("encoded image too large");
        return result;
    } finally { bitmap.close(); }
}
