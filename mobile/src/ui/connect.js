// connect.js — screen 01: sign in with a passkey, or link from the desktop
// by QR link (#id=…&k=…) or hidden code, then compare the two-digit code.
import { app, connect, newIdentity, rememberLinkedIdentity } from "../app.js";
import { Connection } from "../core/connection.js";
import { claimLink, LinkState, linkWithPasskey, passkeysSupported, waitForDecision } from "../core/device.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { scanPairingQR } from "./qr-scanner.js";

// Route cleanup must follow replacement sign-in screens after cancel/failure.
const scanners = new WeakMap();

function brand() {
    return h("div", { class: "brand" },
        h("img", { src: "/app/icons/logo.png", alt: "" }),
        h("h1", {}, "noXa"),
        h("p", {}, t("app.tagline")));
}

function isIOS() {
    return /iPhone|iPad/.test(navigator.userAgent) && !navigator.standalone;
}

export function renderConnect(root, { onSignedIn }) {
    scanners.get(root)?.abort();
    const scanner = new AbortController();
    scanners.set(root, scanner);
    const scan = h("button", { class: "btn btn-block", type: "button", onclick: async () => {
        const link = await scanPairingQR({ signal: scanner.signal });
        if (link && !scanner.signal.aborted && root.isConnected) runLink(root, link, { onSignedIn });
    } }, icon("camera"), t("connect.scan"));
    const error = h("p", { class: "error", role: "alert" });
    const code = h("input", {
        class: "input code", id: "link-code", inputmode: "text", autocomplete: "one-time-code", autocapitalize: "characters",
        spellcheck: "false", maxlength: "9", required: "", minlength: "8", placeholder: t("connect.codePlaceholder"), "aria-describedby": "link-hint",
    });
    const claim = h("button", { class: "btn btn-primary btn-block", type: "submit" }, t("connect.claim"));
    const passkey = h("button", { class: "btn btn-primary btn-block connect-passkey", type: "button", "aria-describedby": "passkey-hint" }, icon("passkey"), " ", t("connect.passkey"));
    if (!passkeysSupported()) {
        passkey.disabled = true;
        passkey.title = t("connect.noPasskeys");
    }
    passkey.onclick = async () => {
        error.textContent = "";
        passkey.disabled = true;
        try {
            const identity = newIdentity();
            const conn = await Connection.open();
            try {
                const result = await linkWithPasskey(conn, identity);
                if (result.state !== LinkState.approved) throw new Error(t("link.expired"));
            } finally {
                conn.close();
            }
            await rememberLinkedIdentity(identity, { via: "passkey" });
            await connect();
            onSignedIn({ offerPasskey: false });
        } catch (err) {
            if (err?.name !== "NotAllowedError") error.textContent = t("signin.failed", { error: err.message || err });
        } finally {
            passkey.disabled = !passkeysSupported();
        }
    };
    const form = h("form", { class: "connect-link", onsubmit: event => {
        event.preventDefault();
        if (code.value.trim().length >= 8) runLink(root, { code: code.value }, { onSignedIn });
    } },
        h("h2", {}, t("connect.link")),
        h("p", { class: "hint", id: "link-hint" }, t("connect.linkHint")),
        scan,
        h("div", { class: "field" }, h("label", { for: "link-code" }, t("connect.code")), code),
        claim);
    const guestName = h("input", { class: "input", id: "guest-name", autocomplete: "nickname", required: "", maxlength: "32", value: app.meta.guest ? app.meta.nickname || "" : "" });
    const guestPassword = h("input", { class: "input", id: "guest-password", type: "password", autocomplete: "current-password", required: "" });
    const guestButton = h("button", { class: "btn btn-block", type: "submit" }, t("connect.guest"));
    const guestForm = app.info?.guest_login ? h("details", { class: "connect-guest" },
        h("summary", {}, t("connect.guest")),
        h("form", { class: "connect-link", onsubmit: async event => {
            event.preventDefault();
            guestButton.disabled = true;
            error.textContent = "";
            try {
                await connect({ guest: { nickname: guestName.value.trim(), serverPassword: guestPassword.value } });
                onSignedIn({ offerPasskey: false });
            } catch (err) {
                error.textContent = t("signin.failed", { error: err.message || err });
            } finally { guestButton.disabled = false; }
        } },
        h("p", { class: "hint" }, t("connect.guestHint")),
        h("div", { class: "field" }, h("label", { for: "guest-name" }, t("connect.nickname")), guestName),
        app.info.server_password_required ? h("div", { class: "field" }, h("label", { for: "guest-password" }, t("connect.serverPassword")), guestPassword) : null,
        guestButton)) : null;
    root.replaceChildren(h("main", { class: "connect connect-signin" },
        brand(),
        h("section", { class: "card connect-card", "aria-labelledby": "signin-heading" },
            h("div", { class: "connect-server" },
                h("div", { class: "connect-server-icon" }, icon("servers")),
                h("div", { class: "connect-server-text" },
                    h("span", { class: "hint" }, t("connect.server")),
                    h("strong", {}, app.info?.server_name || location.host),
                    h("span", { class: "hint mono" }, location.host))),
            h("h2", { id: "signin-heading" }, t("connect.title")),
            passkey,
            h("p", { class: "hint", id: "passkey-hint" }, t(passkeysSupported() ? "connect.passkeyHint" : "connect.noPasskeys")),
            h("div", { class: "connect-divider" }, h("span", {}, t("connect.or"))),
            form,
            guestForm,
            error),
        isIOS() ? h("p", { class: "hint connect-install" }, t("connect.install")) : null));
    return () => scanners.get(root)?.abort();
}

/** Claims a link and waits for the desktop's decision. */
export async function runLink(root, link, { onSignedIn }) {
    scanners.get(root)?.abort();
    const identity = newIdentity();
    const status = h("p", { class: "hint", role: "status" }, t("link.waiting"));
    const abort = new AbortController();
    const cancel = h("button", { class: "btn btn-block", type: "button", onclick: () => { abort.abort(); renderConnect(root, { onSignedIn }); } }, t("link.cancel"));
    root.replaceChildren(h("main", { class: "connect" }, brand(), h("div", { class: "card" }, h("div", { class: "spinner", role: "progressbar", "aria-label": t("link.waiting") })), cancel));
    let conn;
    try {
        conn = await Connection.open();
        const claim = await claimLink(conn, identity, link);
        conn.close();
        if (claim.state !== LinkState.claimed) {
            fail(t("link.expired"));
            return;
        }
        root.replaceChildren(h("main", { class: "connect" }, brand(),
            h("div", { class: "card" },
                h("p", { class: "hint" }, t("link.claimed")),
                h("div", { class: "sas", "aria-label": `${claim.sas[0]} ${claim.sas[1]}` }, claim.sas),
                h("p", { class: "hint mono" }, t("link.desktop", { fingerprint: claim.approverFingerprint })),
                h("div", { style: "display:flex;gap:10px;align-items:center;justify-content:center;margin-top:8px" }, h("div", { class: "spinner" }), status)),
            cancel));
        const state = await waitForDecision(identity, claim.linkID, { signal: abort.signal });
        if (state === LinkState.denied) return fail(t("link.denied"));
        if (state !== LinkState.approved) return fail(t("link.expired"));
        await rememberLinkedIdentity(identity, { via: "link", approverFingerprint: claim.approverFingerprint });
        await connect();
        history.replaceState(null, "", "/app/#/home");
        onSignedIn({ offerPasskey: true });
    } catch (error) {
        conn?.close();
        if (error?.name !== "AbortError") fail(t("signin.failed", { error: error.message || error }));
    }

    function fail(message) {
        renderConnect(root, { onSignedIn });
        toast(message, "warn");
    }
}
