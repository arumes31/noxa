// Upload failures name the pipeline stage that broke (502): reading the local
// file, encrypting it, transferring the ciphertext, or publishing the message
// that references it. Go tags native errors as "[upload:<stage>] <detail>"
// (chat_upload_stage.go); publish failures are tagged here because the send
// is a separate bridge call.
import { t } from "./i18n.js";

export const UPLOAD_STAGES = ["read", "encrypt", "transfer", "publish"];
const STAGE_PREFIX = /^\[upload:([a-z]+)\]\s*/;

export function parseUploadFailure(error, fallbackStage = "transfer") {
    const text = error == null ? "" : typeof error === "string" ? error : String(error.message ?? error);
    const match = STAGE_PREFIX.exec(text);
    const known = !!match && UPLOAD_STAGES.includes(match[1]);
    return { stage: known ? match[1] : fallbackStage, detail: (known ? text.slice(match[0].length) : text).trim() };
}

// describeUploadFailure returns the rendered pieces for one failed item: the
// stage label, the concrete next step and the native detail.
export function describeUploadFailure(error, fallbackStage = "transfer", translate = t) {
    const { stage, detail } = parseUploadFailure(error, fallbackStage);
    const label = translate("upload.stage." + stage);
    return { stage, detail, label, hint: translate("upload.stage." + stage + ".hint"), summary: detail ? label + " — " + detail : label };
}
