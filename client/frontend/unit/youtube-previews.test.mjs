import test from "node:test";
import assert from "node:assert/strict";
import { youtubeVideo } from "../src/youtube-previews.js";

test("YouTube previews support watch, short, live and embed links", () => {
    for (const url of ["https://www.youtube.com/watch?v=Fvr-SJPAh7o", "https://youtu.be/Fvr-SJPAh7o?t=42", "https://m.youtube.com/shorts/Fvr-SJPAh7o", "https://www.youtube.com/live/Fvr-SJPAh7o", "https://www.youtube-nocookie.com/embed/Fvr-SJPAh7o"]) {
        assert.equal(youtubeVideo(url).id, "Fvr-SJPAh7o");
        assert.equal(youtubeVideo(url).thumbnail, "https://i.ytimg.com/vi/Fvr-SJPAh7o/hqdefault.jpg");
    }
});
test("YouTube previews reject lookalike hosts, credentials, ports, malformed IDs and unsafe schemes", () => {
    for (const url of ["https://youtube.com.evil.test/watch?v=Fvr-SJPAh7o", "https://evil.test/youtube.com/Fvr-SJPAh7o", "https://user@youtube.com/watch?v=Fvr-SJPAh7o", "https://youtube.com:8443/watch?v=Fvr-SJPAh7o", "javascript:alert(1)", "file:///youtube.com/watch?v=Fvr-SJPAh7o", "https://youtube.com/watch?v=too-short", "https://youtu.be/Fvr-SJPAh7o%2Fattack", "https://youtube.com/playlist?list=Fvr-SJPAh7o", "", null]) assert.equal(youtubeVideo(url), null, String(url));
});
