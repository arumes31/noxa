import { currentLanguage, t } from "./i18n.js";
import { auditEnglish } from "./audit-messages.js";

export function auditActionLabel(action) {
    const key = `audit.${String(action || "").replace(/^roles\./, "")}`;
    return Object.hasOwn(auditEnglish, key) ? t(key) : t("audit.other");
}

export function parseAuditDetail(detail, structured = false) {
    try {
        const value = JSON.parse(detail);
        if (structured && value?.version === 1 && isRecord(value)
            && (value.text === undefined || typeof value.text === "string")
            && (value.revision === undefined || Number.isSafeInteger(value.revision))
            && validState(value.before) && validState(value.after)) return value;
    } catch { /* Historical free-form records remain plain text. */ }
    return { text: String(detail || "") };
}

function isRecord(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
}

function validState(state, depth = 0) {
    if (state === null || state === undefined) return true;
    if (depth > 2) return false;
    if (Array.isArray(state)) return state.every((role) => isRecord(role) && validState(role, depth + 1));
    if (!isRecord(state)) return false;
    return Object.entries(state).every(([key, value]) => {
        if (value === null || ["string", "number", "boolean"].includes(typeof value)) return true;
        if (key === "access" || key === "settings") return isRecord(value) && validState(value, depth + 1);
        if (!Array.isArray(value)) return false;
        if (key === "permissions") return value.every((v) => typeof v === "string");
        if (key === "role_ids") return value.every(Number.isSafeInteger);
        if (key === "overrides") return value.every((v) => isRecord(v) && typeof v.capability === "string"
            && ["allow", "deny"].includes(v.effect) && Number.isSafeInteger(v.role_id ?? v.user_id));
        return false;
    });
}

export function auditResource(action, detail) {
    const state = isRecord(detail.after) ? detail.after : detail.before;
    if (!isRecord(state)) return "";
    const resource = (label, id, name) => `${t(label)}${typeof name === "string" && name ? ` ${name}` : ""}${Number.isSafeInteger(id) ? ` (#${id})` : ""}`;
    if (/^roles\.role_(create|update|delete)$/.test(action)) return resource("audit.role", state.id, state.name);
    if (action === "roles.member_roles_set") return resource("audit.member", state.user_id);
    if (/^roles\.channel_/.test(action)) return resource("audit.channel", state.access?.channel_id ?? state.channel_id, state.settings?.Name);
    return "";
}

function fieldKey(key) {
    return key.replace(/([a-z])([A-Z])/g, "$1_$2").toLowerCase();
}

export function auditChanges(before, after, capabilities = []) {
    const permission = (key) => {
        const entry = capabilities.find((c) => c.key === key);
        return entry?.[currentLanguage()] || entry?.en || t("audit.permission");
    };
    const valueText = (key, value) => {
        if (value === null || value === undefined || value === "") return t("audit.none");
        if (typeof value === "boolean") return t(value ? "audit.yes" : "audit.no");
        if (key === "channel_type") return t(["audit.temporary", "audit.semiPermanent", "audit.permanent"][value] || "audit.unavailable");
        if (Array.isArray(value)) {
            if (key === "permissions") return value.map(permission).join(", ") || t("audit.none");
            if (key === "overrides") return value.filter(isRecord).map((o) => `${t(o.role_id ? "audit.role" : "audit.member")} #${o.role_id || o.user_id}: ${permission(o.capability)} — ${t(o.effect === "allow" ? "audit.allow" : "audit.deny")}`).join("; ") || t("audit.none");
            return value.map((v) => `#${v}`).join(", ") || t("audit.none");
        }
        return typeof value === "object" ? t("audit.unavailable") : String(value);
    };
    const flatten = (state, prefix = "", rows = new Map()) => {
        if (!state || typeof state !== "object") return rows;
        if (Array.isArray(state)) {
            for (const role of state.filter(isRecord)) rows.set(`${t("audit.role")} ${role.name || ""} (#${role.id})`, String(role.position));
            return rows;
        }
        for (const [raw, value] of Object.entries(state)) {
            const key = fieldKey(raw);
            const label = Object.hasOwn(auditEnglish, `audit.${key}`) ? t(`audit.${key}`) : t("audit.field");
            if (key === "settings" || key === "access") flatten(value, `${prefix}${label} / `, rows);
            else rows.set(prefix + label, valueText(key, value));
        }
        return rows;
    };
    const old = flatten(before), next = flatten(after);
    return [...new Set([...old.keys(), ...next.keys()])]
        .filter((label) => old.get(label) !== next.get(label))
        .map((label) => ({ label, before: old.get(label) || t("audit.none"), after: next.get(label) || t("audit.none") }));
}
