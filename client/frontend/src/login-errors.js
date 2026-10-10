import { t } from "./i18n.js";

// Older servers/native bridges return reason strings. Match only known reasons;
// an unknown rejection must not be presented as an incorrect password.
export function loginFailure(error, request = {}) {
    const reason = String(error?.message || error || "").trim().replace(/^Error: /, "").toLowerCase();
    if (reason === "invalid server password") return { kind: "serverPassword", field: "login-serverpw", invalid: true };
    if (reason === "invalid credentials" || reason === "account password is incorrect") return { kind: "account", field: "login-accountpw", invalid: true };
    if (reason === "authentication failed") return { kind: "authentication", field: "login-accountpw" };
    if (reason === "banned") return { kind: "banned" };
    if (reason === "too many failed logins, try again later") return { kind: "rateLimit" };
    if (reason === "server address is required") return { kind: "addressRequired", field: "login-addr", invalid: true };
    if (reason === "nickname is required") return { kind: "nameRequired", field: "login-nick", invalid: true };
    if (reason.startsWith("display name must ")) return { kind: "displayName", field: request.displayName ? "login-display-name" : "login-nick", invalid: true };
    if (["saved password is no longer available; enter it again", "saved passwords cannot be unlocked by this os account; enter them again", "password storage is unavailable", "os password protection is unavailable"].includes(reason)) {
        return { kind: "savedPassword", field: request.use_saved_server && !request.use_saved_account ? "login-serverpw" : "login-accountpw" };
    }
    if (/^(tls[: ]|x509:|certificate |server certificate |trust store )/.test(reason)) return { kind: "certificate" };
    if (/^(dial (tcp|udp)|read (tcp|udp)|write (tcp|udp)|lookup )/.test(reason) || /^(context deadline exceeded|i\/o timeout|connection refused|network is unreachable)$/.test(reason)) return { kind: "network", field: "login-addr" };
    if (reason === "unsupported server authorization model; upgrade noxa") return { kind: "version" };
    return { kind: "unknown" };
}

export function createLoginFeedback($) {
    const form = document.querySelector(".login-card");
    const fields = ["login-addr", "login-nick", "login-display-name", "login-accountpw", "login-serverpw"].map($);
    let revision = 0, current = null;
    function reset() {
        $("login-error").replaceChildren();
        for (const field of fields) {
            const id = `${field.id}-error`;
            $(id)?.remove();
            const description = (field.getAttribute("aria-describedby") || "").split(/\s+/).filter(value => value && value !== id);
            if (description.length) field.setAttribute("aria-describedby", description.join(" "));
            else field.removeAttribute("aria-describedby");
            field.removeAttribute("aria-invalid");
        }
    }
    function clear() { revision++; current = null; reset(); }
    function render(focus) {
        reset();
        if (!current) return;
        const { error, request } = current, failure = loginFailure(error, request);
        const area = $("login-error");
        if (failure.kind === "unknown") { area.textContent = String(error?.message || error); return; }
        const message = document.createElement("p"); message.textContent = t(`login.failure.${failure.kind}`); area.append(message);
        const action = t(`login.recovery.${failure.kind}`);
        const field = failure.field && $(failure.field);
        if (field) {
            const hint = document.createElement("small"); hint.id = `${field.id}-error`; hint.className = "login-field-error"; hint.textContent = action;
            field.closest("label").after(hint);
            field.setAttribute("aria-describedby", [field.getAttribute("aria-describedby"), hint.id].filter(Boolean).join(" "));
            if (failure.invalid) field.setAttribute("aria-invalid", "true");
            if (field.closest("#login-options")) $("login-options").open = true;
            if (focus) {
                field.focus();
                hint.scrollIntoView({ block: "nearest", inline: "nearest" });
            }
        } else {
            const hint = document.createElement("p"); hint.textContent = action; area.append(hint);
        }
        // Native reasons remain available for support; never interpret server text as HTML.
        if (!["addressRequired", "nameRequired"].includes(failure.kind)) {
            const detail = document.createElement("details"), summary = document.createElement("summary"), reason = document.createElement("p");
            summary.textContent = t("login.failureDetails"); reason.textContent = String(error?.message || error);
            detail.append(summary, reason); area.append(detail);
        }
    }
    // Clear before a field handler starts work; a fast native rejection must
    // not be erased later by the same bubbling change event.
    form.addEventListener("input", clear, true);
    form.addEventListener("change", clear, true);
    window.addEventListener("noxa-language-changed", () => { if (current) render(false); });
    return {
        clear,
        show(error, request = {}) { current = { error, request }; render(true); },
        snapshot() {
            const captured = revision, values = fields.map(field => field.value);
            return () => captured === revision && fields.every((field, i) => field.value === values[i]) && !$("login-overlay").classList.contains("hidden");
        },
    };
}
