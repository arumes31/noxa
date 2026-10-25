import test from "node:test";
import assert from "node:assert/strict";
import {
    blockedPreviewHost,
    blockedPreviewPlaceholder,
    isServerInlinePreviewImage,
    isServerProxiedPreviewURL,
    previewServerOrigin,
    resolvePreviewImage,
} from "../src/preview-source-policy.js";
import { previewImage } from "../src/preview-image.js";
import { renderYouTubePreviews } from "../src/youtube-previews.js";
import { renderVideoPortalPreviews } from "../src/video-portal-previews.js";

const JPEG = "data:image/jpeg;base64,/9j/2Q==";
const SERVER = "https://voice.example.test:8443";

test("only the connected server's origin is accepted as a preview proxy origin", () => {
    assert.equal(previewServerOrigin(SERVER), SERVER);
    assert.equal(previewServerOrigin(SERVER + "/"), SERVER);
    for (const value of ["http://voice.example.test", "https://user@voice.example.test", "https://voice.example.test/path", "https://voice.example.test/?q=1", "https://voice.example.test/#x", "voice.example.test", "", null, 42]) {
        assert.equal(previewServerOrigin(value), null, String(value));
    }
});

test("inline server JPEG bytes are the only data URLs allowed", () => {
    assert.equal(isServerInlinePreviewImage(JPEG), true);
    for (const value of ["data:image/png;base64,iVBORw0KGgo=", "data:image/svg+xml;base64,PHN2Zy8+", "data:image/jpeg;base64,/9j/2Q==<", "data:image/jpeg;base64," + "A".repeat(280001), "DATA:image/jpeg;base64,/9j/2Q==", "", null]) {
        assert.equal(isServerInlinePreviewImage(value), false, String(value));
    }
});

test("third-party hosts are never a proxied preview source, with or without a server origin", () => {
    const thirdParty = ["https://i.ytimg.com/vi/x/hqdefault.jpg", "https://pbs.twimg.com/media/x.jpg", "https://static-cdn.jtvnw.net/x.jpg", "http://voice.example.test:8443/preview/x", "https://voice.example.test.evil.test/preview/x", "https://voice.example.test/preview/x", "blob:https://voice.example.test/uuid", "javascript:alert(1)", "//voice.example.test:8443/preview/x", "/preview/x"];
    for (const url of thirdParty) {
        assert.equal(isServerProxiedPreviewURL(url), false, url);
        assert.equal(isServerProxiedPreviewURL(url, SERVER), false, url);
    }
    assert.equal(isServerProxiedPreviewURL(JPEG), true);
    assert.equal(isServerProxiedPreviewURL(JPEG, SERVER), true);
});

test("the server origin only unlocks its own reserved proxy paths", () => {
    assert.equal(isServerProxiedPreviewURL(SERVER + "/preview/abc-123", SERVER), true);
    assert.equal(isServerProxiedPreviewURL(SERVER + "/dl/0123456789abcdef0123456789abcdef", SERVER), true);
    for (const url of [SERVER + "/preview/", SERVER + "/preview/a/b", SERVER + "/preview/a?x=1", SERVER + "/preview/a#h", SERVER + "/healthz", SERVER + "/", "https://user:pw@voice.example.test:8443/preview/a", SERVER + "/preview/" + "a".repeat(2100)]) {
        assert.equal(isServerProxiedPreviewURL(url, SERVER), false, url);
    }
    // A broken origin never widens the policy.
    assert.equal(isServerProxiedPreviewURL(SERVER + "/preview/abc", "voice.example.test:8443"), false);
});

test("resolvePreviewImage distinguishes allowed, blocked and absent images", () => {
    assert.deepEqual(resolvePreviewImage({ thumbnail: JPEG }), { src: JPEG, blocked: false, host: "" });
    assert.deepEqual(resolvePreviewImage(JPEG), { src: JPEG, blocked: false, host: "" });
    assert.deepEqual(resolvePreviewImage({ thumbnail: "https://i.ytimg.com/vi/x/hq.jpg?sig=secret" }), { src: null, blocked: true, host: "i.ytimg.com" });
    assert.deepEqual(resolvePreviewImage({ image: "https://pbs.twimg.com/x.jpg" }), { src: null, blocked: true, host: "pbs.twimg.com" });
    assert.deepEqual(resolvePreviewImage({ thumbnail: "data:image/png;base64,iVBORw0KGgo=" }), { src: null, blocked: true, host: "" });
    assert.deepEqual(resolvePreviewImage({ thumbnail: "not a url" }), { src: null, blocked: true, host: "" });
    for (const meta of [{ thumbnail: "" }, { thumbnail: "   " }, {}, null, undefined, "", 7, { thumbnail: 7 }]) {
        assert.deepEqual(resolvePreviewImage(meta), { src: null, blocked: false, host: "" }, JSON.stringify(meta));
    }
    assert.equal(resolvePreviewImage({ thumbnail: SERVER + "/preview/a" }, SERVER).src, SERVER + "/preview/a");
    assert.equal(previewImage(JPEG), JPEG);
    assert.equal(previewImage("https://i.ytimg.com/x.jpg"), null);
});

test("the blocked host exposes only a bounded hostname", () => {
    assert.equal(blockedPreviewHost("https://cdn.example.test/private/path?token=1"), "cdn.example.test");
    assert.equal(blockedPreviewHost("https://" + "a".repeat(200) + ".test/x"), "a".repeat(80));
    for (const value of ["data:text/html,<b>", "blob:https://x/y", "javascript:alert(1)", "vbscript:msgbox(1)", "file:///C:/x", "ftp://files.example.test/a", "nope", "", null]) assert.equal(blockedPreviewHost(value), "", String(value));
});

// Minimal DOM double shared by the renderer tests below.
function element(tagName) {
    const attributes = new Map();
    return {
        tagName, children: [], dataset: {}, hidden: false, isConnected: true, textContent: "", className: "", title: "",
        get src() { return this._src || ""; },
        set src(value) { this._src = value; this.srcAssignments = (this.srcAssignments || 0) + 1; },
        append(...nodes) { this.children.push(...nodes); },
        appendChild(node) { this.children.push(node); return node; },
        insertBefore(node, before) { const at = this.children.indexOf(before); this.children.splice(at < 0 ? this.children.length : at, 0, node); return node; },
        remove() { this.removed = true; },
        removeAttribute(name) { attributes.delete(name); if (name === "src") delete this._src; },
        setAttribute(name, value) { attributes.set(name, String(value)); },
        getAttribute(name) { return attributes.get(name) ?? null; },
        attributes,
    };
}

function findAll(node, predicate, out = []) {
    for (const child of node.children || []) {
        if (predicate(child)) out.push(child);
        findAll(child, predicate, out);
    }
    return out;
}

function installDOM(t, result, method) {
    const previous = { document: globalThis.document, window: globalThis.window };
    globalThis.document = { createElement: element };
    globalThis.window = { go: { main: { App: { [method]: async () => result } } } };
    t.after(() => { globalThis.document = previous.document; globalThis.window = previous.window; });
}

function container(url) {
    const root = element("div");
    root.querySelectorAll = () => [{ dataset: { url } }];
    return root;
}

const scope = { generation: 1, tabID: "tab-policy-" + Math.random() };
const flush = () => new Promise(resolve => setTimeout(resolve, 10));

test("YouTube cards never load a third-party thumbnail and show the blocked marker instead", async (t) => {
    installDOM(t, { title: "Clip", author: "Author", thumbnail: "https://i.ytimg.com/vi/Fvr-SJPAh7o/hqdefault.jpg" }, "YouTubePreviewForTab");
    const root = container("https://www.youtube.com/watch?v=Fvr-SJPAh7o");
    renderYouTubePreviews(root, { enabled: true, scope, isCurrent: () => true });
    await flush();
    const images = findAll(root, node => node.tagName === "img");
    assert.equal(images.length, 1);
    assert.equal(images[0].src, "", "no third-party URL reached <img src>");
    assert.equal(images[0].hidden, true);
    const markers = findAll(root, node => node.className.startsWith("preview-blocked"));
    assert.equal(markers.length, 1);
    assert.match(markers[0].textContent, /server|Server/);
    assert.match(markers[0].title, /i\.ytimg\.com/);
    assert.equal(markers[0].dataset.blockedHost, "i.ytimg.com");
    assert.equal(findAll(root, node => node.className === "youtube-title")[0].textContent, "Clip");
});

test("YouTube cards still load server-transcoded bytes without a marker", async (t) => {
    installDOM(t, { title: "Clip", thumbnail: JPEG }, "YouTubePreviewForTab");
    const root = container("https://youtu.be/Fvr-SJPAh7o");
    renderYouTubePreviews(root, { enabled: true, scope: { ...scope, tabID: scope.tabID + "-ok" }, isCurrent: () => true });
    await flush();
    const [image] = findAll(root, node => node.tagName === "img");
    assert.equal(image.src, JPEG);
    assert.equal(image.hidden, false);
    assert.equal(findAll(root, node => node.className.startsWith("preview-blocked")).length, 0);
});

test("video portal cards block CDN thumbnails visibly", async (t) => {
    installDOM(t, { title: "Stream", author: "Someone", thumbnail: "https://static-cdn.jtvnw.net/previews-ttv/live_user_twitch.jpg" }, "VideoPortalPreviewForTab");
    const root = container("https://www.twitch.tv/twitch");
    renderVideoPortalPreviews(root, { enabled: true, scope: { ...scope, tabID: scope.tabID + "-portal" }, isCurrent: () => true });
    await flush();
    const images = findAll(root, node => node.tagName === "img");
    assert.equal(images[0].src, "");
    assert.equal(images[0].removed, true);
    const markers = findAll(root, node => node.className.startsWith("preview-blocked"));
    assert.equal(markers.length, 1);
    assert.match(markers[0].title, /static-cdn\.jtvnw\.net/);
    // The provider placeholder keeps the card readable behind the marker.
    assert.equal(findAll(root, node => node.className === "portal-placeholder")[0].hidden, false);
});

test("the placeholder is text-only markup with a tooltip", () => {
    const marker = blockedPreviewPlaceholder({ createElement: element }, { label: "Vorschau blockiert: nur über Server", title: "why", className: "extra", host: "cdn.example" });
    assert.equal(marker.className, "preview-blocked extra");
    assert.equal(marker.textContent, "Vorschau blockiert: nur über Server");
    assert.equal(marker.title, "why");
    assert.equal(marker.getAttribute("role"), "note");
    assert.equal(marker.dataset.blockedHost, "cdn.example");
    assert.equal(blockedPreviewPlaceholder(null, { label: "x" }), null);
});
