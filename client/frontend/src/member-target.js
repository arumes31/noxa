// Identity stays attached to the element, never inferred from display names.
export function memberTarget(element, uid, name, { clientID = "", openOnClick = true } = {}) {
    if (!uid) return;
    element.dataset.memberUid = uid;
    element.dataset.memberName = name || uid;
    if (clientID) element.dataset.memberClientId = clientID;
    else delete element.dataset.memberClientId;
    element.tabIndex = 0;
    element.setAttribute("aria-haspopup", "menu");
    if (element.tagName !== "BUTTON") element.setAttribute("role", "button");
    if (!openOnClick) return;
    element.onclick = event => {
        event.stopPropagation();
        element.dispatchEvent(new CustomEvent("noxa:member-menu", { bubbles: true }));
    };
    if (element.tagName !== "BUTTON") element.onkeydown = event => {
        if (event.key === "Enter" || event.key === " ") { event.preventDefault(); element.click(); }
    };
}
