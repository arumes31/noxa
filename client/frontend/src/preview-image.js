import { resolvePreviewImage } from "./preview-source-policy.js";

// Only server-transcoded JPEG bytes may become a preview image source. The
// decision lives in preview-source-policy.js; this keeps the historic helper
// for callers that only need the accepted source or null.
export function previewImage(value) {
    return resolvePreviewImage(value).src;
}
