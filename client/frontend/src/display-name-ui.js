import { closeDialog, mountServerDialog, isCurrentServerDialog } from "./modal.js";
import { updateLocalSettings } from "./settings-store.js";
import { t } from "./i18n.js";

export function openDisplayNameDialog() {
    const v = window.__noxa;
    const { state } = v;
    const tabID = state.activeTabID;
    const connected = !!state.myClientID;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `<form class="dlg">
        <h3>${t("menu.nicknameTitle")}</h3>
        <label class="dlg-label" for="display-name-value">${t("menu.nicknamePrompt")}</label>
        <input id="display-name-value" class="dlg-input" autocomplete="nickname" aria-describedby="display-name-hint" />
        <p id="display-name-hint" class="hint">${t("login.displayNameHint")}</p>
        <p class="dlg-error" role="alert"></p>
        <div class="dlg-buttons"><button type="submit" class="dlg-ok">${t("common.save")}</button>
        <button type="button" class="dlg-cancel">${t("common.cancel")}</button></div>
    </form>`;
    const input = overlay.querySelector("input");
    input.value = state.myNickname || state.settings?.display_name || "";
    const button = overlay.querySelector(".dlg-ok");
    const errorEl = overlay.querySelector(".dlg-error");
    overlay.querySelector(".dlg-cancel").onclick = () => closeDialog(overlay);
    overlay.querySelector("form").onsubmit = async event => {
        event.preventDefault();
        if (button.disabled) return;
        const name = input.value.trim();
        if (!name || Array.from(name).length > 64 || /[\p{Cc}\p{Cs}]/u.test(input.value)) {
            errorEl.textContent = t("menu.displayNameInvalid");
            return;
        }
        button.disabled = input.disabled = true;
        button.textContent = t("common.saving");
        errorEl.textContent = "";
        try {
            if (connected) {
                const rename = window.go.main.App.SetDisplayNameForTab;
                if (typeof rename !== "function") throw new Error(t("menu.displayNameUnavailable"));
                const error = await rename(tabID, name);
                if (!isCurrentServerDialog(overlay)) return;
                if (error) throw new Error(error);
                state.myNickname = name;
                const connection = state.tabConnects.get(tabID) || state.lastConnect;
                if (connection) connection.displayName = name;
                if (state.lastSuccessfulConnect?.addr === connection?.addr && state.lastSuccessfulConnect?.nick === connection?.nick) {
                    state.lastSuccessfulConnect.displayName = name;
                }
            }
            try { await updateLocalSettings(s => { s.display_name = name; }); }
            catch (error) {
                if (!connected) throw error;
                if (isCurrentServerDialog(overlay)) v.toast(t("menu.saveFailed", { error: String(error) }), "warn");
            }
            if (!isCurrentServerDialog(overlay)) return;
            document.getElementById("login-display-name").value = name;
            v.toast(t(connected ? "menu.nicknameSet" : "menu.displayNameNext", { nickname: name }));
            closeDialog(overlay);
        } catch (error) {
            if (isCurrentServerDialog(overlay)) errorEl.textContent = error.message || String(error);
        } finally {
            if (isCurrentServerDialog(overlay)) {
                button.disabled = input.disabled = false;
                button.textContent = t("common.save");
            }
        }
    };
    mountServerDialog(overlay, { initialFocus: input });
}
