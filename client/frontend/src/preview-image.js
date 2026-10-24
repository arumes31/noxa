// Only server-transcoded JPEG bytes may become a preview image source.
export function previewImage(value) {
    return typeof value === "string" && value.length <= 280000 && /^data:image\/jpeg;base64,[A-Za-z0-9+/]+={0,2}$/.test(value) ? value : null;
}
