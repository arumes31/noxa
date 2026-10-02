import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";

let incomingDialog = null;

// Keep consecutive pokes in one dismissible popup instead of stacking modals.
export function showIncomingPoke({ sender, message }) {
    if (incomingDialog && !isCurrentServerDialog(incomingDialog)) closeDialog(incomingDialog);
    if (!incomingDialog) {
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay incoming-poke";
        overlay.setAttribute("aria-describedby", "incoming-poke-list");
        overlay.innerHTML = `
            <div class="dlg dlg-wide">
                <h3></h3>
                <div id="incoming-poke-list" role="log" aria-live="polite" aria-relevant="additions" tabindex="0"></div>
                <div class="dlg-buttons"><button type="button" class="dlg-ok"></button></div>
            </div>`;
        overlay.querySelector("h3").textContent = t("poke.received");
        const ok = overlay.querySelector(".dlg-ok");
        ok.textContent = t("common.ok");
        ok.onclick = () => closeDialog(overlay);
        incomingDialog = overlay;
        mountServerDialog(overlay, {
            initialFocus: ok,
            onClose: () => { if (incomingDialog === overlay) incomingDialog = null; },
        });
    }

    const list = incomingDialog.querySelector("#incoming-poke-list");
    const entry = document.createElement("div");
    entry.className = "incoming-poke-entry";
    const from = document.createElement("strong");
    from.textContent = t("poke.from", { name: sender || t("poke.someone") });
    const body = document.createElement("p");
    body.textContent = message || t("poke.noMessage");
    entry.append(from, body);
    list.appendChild(entry);
    // Bound an unattended popup like the notification center.
    if (list.childElementCount > 50) list.firstElementChild.remove();
    list.scrollTop = list.scrollHeight;
}
