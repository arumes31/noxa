import { t } from "./i18n.js";

// Passwords stay in the native credential store. Only their presence crosses
// the bridge; changing the server/account invalidates every pending lookup.
export function createLoginMemory({ $, settings, clearBookmark, onSelect }) {
    const app = () => window.go.main.App;
    const addr = $("login-addr"), nick = $("login-nick"), display = $("login-display-name");
    const account = $("login-accountpw"), server = $("login-serverpw");
    const remember = $("login-remember-passwords"), hint = $("login-password-hint");
    let edited = false, epoch = 0, status = {}, pending = Promise.resolve();
    let accountEdited = false, serverEdited = false;
    const scope = () => JSON.stringify([addr.value.trim(), nick.value.trim()]);

    function labels() {
        account.placeholder = t(status.account_saved && !accountEdited ? "login.savedPassword" : "login.accountPasswordPlaceholder");
        server.placeholder = t(status.server_saved && !serverEdited ? "login.savedPassword" : "workspace.labels.ifRequired");
        hint.textContent = status.error || t(status.available ? "login.passwordProtection" : "login.passwordUnavailable");
    }

    function lookup() {
        const requestEpoch = ++epoch, key = scope();
        status = {};
        remember.checked = false;
        remember.disabled = true;
        labels();
        pending = (async () => {
            let result;
            try { result = await app().GetLoginPasswordStatus(addr.value.trim(), nick.value.trim()); }
            catch { result = { supported: true, error: t("login.passwordLookupFailed") }; }
            if (epoch !== requestEpoch || scope() !== key) return;
            status = result || {};
            remember.disabled = !status.available;
            remember.checked = !!(status.account_saved || status.server_saved);
            labels();
        })();
        return pending;
    }

    function clearPasswords() {
        account.value = server.value = "";
        accountEdited = serverEdited = false;
    }

    function select(profile) {
        onSelect?.();
        edited = true;
        clearBookmark();
        addr.value = profile.addr || "";
        nick.value = profile.nickname || "";
        display.value = profile.display_name ?? profile.nickname_override ?? "";
        clearPasswords();
        return lookup();
    }

    for (const input of [addr, nick]) input.addEventListener("input", () => {
        edited = true;
        clearBookmark();
        clearPasswords();
        const recent = settings()?.recents?.find(r => r.addr === addr.value.trim() && r.nickname === nick.value.trim());
        display.value = recent?.display_name || "";
        void lookup();
    });
    display.addEventListener("input", () => { edited = true; });
    account.addEventListener("input", () => { edited = accountEdited = true; labels(); });
    server.addEventListener("input", () => { edited = serverEdited = true; labels(); });
    remember.addEventListener("change", () => {
        edited = true;
        if (remember.checked) return;
        const key = scope(), requestEpoch = ++epoch;
        remember.disabled = true;
        pending = (async () => {
            let error;
            try { error = await app().ForgetLoginPasswords(addr.value.trim(), nick.value.trim()); }
            catch { error = t("login.passwordForgetFailed"); }
            if (epoch !== requestEpoch || scope() !== key) return;
            remember.disabled = !status.available;
            if (error) {
                remember.checked = !!(status.account_saved || status.server_saved);
                $("login-error").textContent = error;
            } else {
                status.account_saved = status.server_saved = false;
                status.error = "";
            }
            labels();
        })();
    });
    window.addEventListener("noxa-language-changed", () => queueMicrotask(labels));
    void lookup();

    return {
        select,
        connected(request) {
            if (!request || request.addr !== addr.value.trim() || request.nickname !== nick.value.trim()) return;
            if (account.value && account.value !== request.password || server.value !== request.server_password) return;
            // A native save may have created a password that was not present
            // before this attempt. Refresh metadata after clearing typed secrets.
            clearPasswords();
            void lookup();
        },
        restore(value) {
            if (!edited && value?.recents?.[0]) return select(value.recents[0]);
        },
        async request() {
            const key = scope(), requestEpoch = epoch;
            const values = { addr: addr.value.trim(), nickname: nick.value.trim(), display_name: display.value.trim(),
                password: account.value, server_password: server.value };
            const useAccount = !accountEdited && !account.value, useServer = !serverEdited && !server.value;
            await pending;
            if (epoch !== requestEpoch || scope() !== key) throw new Error(t("login.connectionChanged"));
            if (!status.supported) return null; // older native bridge
            return {
                ...values,
                remember_passwords: remember.checked && !!status.available,
                use_saved_account: !!status.account_saved && useAccount,
                use_saved_server: !!status.server_saved && useServer,
            };
        },
    };
}
