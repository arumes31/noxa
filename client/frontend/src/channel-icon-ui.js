import { t } from "./i18n.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { roleButton, roleElement } from "./role-editor-view.js";
import { prepareIcon } from "./image-tools.js";
import { imageDataURL, setSafeImage } from "./safe-media.js";
import "./roles.css";

export function openChannelIcon(channelID) {
    const app = window.go.main.App, tabID = window.__noxa.state.activeTabID;
    const overlay = roleElement("div", "dlg-overlay");
    const dialog = roleElement("form", "dlg roles-dialog channel-icon-dialog");
    dialog.onsubmit = event => event.preventDefault();
    const header = roleElement("header", "roles-header");
    const close = roleButton(t("common.close"), () => closeDialog(overlay, "cancel"));
    header.append(roleElement("h3", "", t("roles.iconEditor.title")), close);
    const content = roleElement("div", "channel-access-content");
    const preview = roleElement("div", "channel-icon-preview");
    const uploadLabel = roleElement("label", "role-field", t("roles.iconEditor.upload"));
    const upload = roleElement("input", "dlg-input"); upload.type = "file"; upload.accept = "image/png,image/jpeg,image/gif,image/webp";
    uploadLabel.append(upload);
    const copyLabel = roleElement("label", "role-field", t("roles.iconEditor.reuse"));
    const copy = roleElement("select", "dlg-input"); copyLabel.append(copy);
    const status = roleElement("p", "role-status"); status.setAttribute("role", "status");
    const error = roleElement("p", "role-error"); error.setAttribute("role", "alert");
    let busy = false, saving = false, loaded = false, needsRefresh = false, draft = null;
    const current = () => isCurrentServerDialog(overlay);
    const controls = () => {
        upload.disabled = copy.disabled = busy || !loaded || needsRefresh;
        save.disabled = busy || !loaded || !draft || needsRefresh;
        refresh.disabled = busy;
        close.disabled = saving;
        overlay.dataset.blocking = String(saving);
        status.textContent = busy ? t(saving ? "roles.saving" : "roles.channel.loading") : "";
    };
    const showImage = asset => {
        const url = imageDataURL(asset);
        setSafeImage(preview, url, { alt: t("roles.iconEditor.preview") });
        if (!url) preview.append(roleElement("p", "role-hint", t("roles.iconEditor.empty")));
    };
    const read = async id => {
        const asset = await app.ChannelIconGetForTab(tabID, id);
        if (!asset || asset.channel_id !== id) throw new Error("icon response mismatch");
        return asset;
    };
    const load = async () => {
        if (busy || !current()) return;
        busy = true; controls();
        try {
            const asset = await read(channelID);
            if (!current()) return;
            showImage(asset); draft = null; loaded = true; needsRefresh = false; error.textContent = ""; upload.value = "";
            copy.replaceChildren();
            const placeholder = roleElement("option", "", t("roles.iconEditor.choose")); placeholder.value = "0"; placeholder.disabled = true; copy.append(placeholder);
            for (const channel of window.__noxa.state.channels || []) {
                if (!channel.HasIcon || channel.ChannelID === channelID) continue;
                const option = roleElement("option", "", channel.Name); option.value = String(channel.ChannelID); copy.append(option);
            }
            copy.value = "0";
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.iconEditor.failed"); } }
        finally { if (current()) { busy = false; controls(); } }
    };
    upload.onchange = async () => {
        if (busy || needsRefresh || !current()) return;
        busy = true; error.textContent = ""; controls();
        try {
            const image = await prepareIcon(upload.files[0], 1024, 0.85, current);
            if (!image || !current()) return;
            draft = { data: image.dataBase64, source: 0 }; copy.value = "0";
            showImage({ data_base64: image.dataBase64, content_type: image.contentType });
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.iconEditor.failed"); } }
        finally { if (current()) { busy = false; upload.value = ""; controls(); } }
    };
    copy.onchange = async () => {
        if (busy || needsRefresh || !current()) return;
        const source = Number(copy.value);
        if (!source) return;
        busy = true; error.textContent = ""; controls();
        try {
            const asset = await read(source);
            if (!current()) return;
            if (!imageDataURL(asset)) throw new Error("source icon unavailable");
            draft = { data: "", source }; showImage(asset);
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.iconEditor.failed"); } }
        finally { if (current()) { busy = false; controls(); } }
    };
    const save = roleButton(t("roles.iconEditor.save"), async () => {
        if (busy || !draft || needsRefresh || !current()) return;
        busy = saving = true; error.textContent = ""; controls();
        try {
            await app.SetRoleChannelIconForTab(tabID, channelID, draft.data, draft.source);
            if (current()) { draft = null; closeDialog(overlay, "saved"); }
        } catch { if (current()) { needsRefresh = true; error.textContent = t("roles.iconEditor.failed"); } }
        finally { if (current()) { busy = saving = false; controls(); } }
    });
    const discard = () => confirmDialog({ title: t("roles.iconEditor.title"), message: t("roles.channel.discard"), confirmLabel: t("common.apply"), cancelLabel: t("common.cancel"), serverScoped: true });
    const refresh = roleButton(t("roles.refresh"), async () => {
        if ((!draft || await discard()) && current()) load();
    });
    const footer = roleElement("footer", "roles-footer"); footer.append(status, refresh, save);
    content.append(preview, uploadLabel, copyLabel, roleElement("p", "role-hint", t("roles.iconEditor.help")));
    dialog.append(header, content, error, footer); overlay.append(dialog);
    mountServerDialog(overlay, { onCancel: () => {
        if (saving) return false;
        if (!draft) return true;
        discard().then(ok => { if (ok && current()) closeDialog(overlay); });
        return false;
    } });
    load();
}
