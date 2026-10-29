// Keep finished audio files individually inspectable in desktop release bundles.
import { fileURLToPath } from "node:url";
import { mediaE2EENoticePlugin } from "../../tools/media-e2ee-notices.mjs";

export default {
    plugins: [mediaE2EENoticePlugin(fileURLToPath(new URL("./node_modules/", import.meta.url)))],
    build: { assetsInlineLimit: 0, minify: "oxc" },
};
