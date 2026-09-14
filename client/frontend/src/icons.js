// Shared 24px outline icons. Names and paths are fixed application assets.
const paths = {
    mic: '<rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10v2a7 7 0 0 0 14 0v-2M12 19v3M8 22h8"/>',
    micOff: '<path d="m3 3 18 18M9 9v3a3 3 0 0 0 5 2M9 5a3 3 0 0 1 6 0v5M5 10v2a7 7 0 0 0 12 5M19 10v2M12 19v3M8 22h8"/>',
    headphones: '<path d="M3 14v-3a9 9 0 0 1 18 0v3"/><rect x="3" y="12" width="4" height="9" rx="2"/><rect x="17" y="12" width="4" height="9" rx="2"/>',
    headphonesOff: '<path d="M3 14v-3a9 9 0 0 1 18 0v3"/><rect x="3" y="12" width="4" height="9" rx="2"/><rect x="17" y="12" width="4" height="9" rx="2"/><path class="icon-off-slash" d="m3 21 18-18"/>',
    camera: '<rect x="2" y="5" width="14" height="14" rx="2"/><path d="m16 9 6-4v14l-6-4"/>',
    cameraOff: '<path d="m3 3 18 18M9 5h5a2 2 0 0 1 2 2v2l6-4v14l-6-4M16 16v1a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V7M2 7l14 9"/>',
    screen: '<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M12 17v4M8 21h8"/>',
    speaker: '<path d="m11 3-6 5H2v8h3l6 5zM15 8a6 6 0 0 1 0 8M18 4a11 11 0 0 1 0 16"/>',
    settings: '<path d="m9 3 1-1h4l1 3 3 1 3-1 2 4-2 2v3l2 2-2 4-3-1-3 1-1 3h-4l-1-3-3-1-3 1-2-4 2-2v-3L1 9l2-4 3 1 3-1z"/><circle cx="12" cy="12" r="3"/>',
    users: '<circle cx="9" cy="7" r="4"/><path d="M2 21v-3a7 7 0 0 1 14 0v3M17 3a4 4 0 0 1 0 8M19 14a6 6 0 0 1 3 5v2"/>',
    search: '<circle cx="10" cy="10" r="7"/><path d="m15 15 7 7"/>',
    chat: '<path d="M21 11a9 9 0 0 1-9 9H3l1-5a9 9 0 1 1 17-4Z"/>',
    file: '<path d="M14 2H5v20h14V7zM14 2v5h5"/>',
    close: '<path d="m6 6 12 12M6 18 18 6"/>',
    trash: '<path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7M14 10v7"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    chevron: '<path d="m6 9 6 6 6-6"/>',
    more: '<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>',
    disconnect: '<path d="M3 16v-4c5-5 13-5 18 0v4h-5v-4H8v4z"/>',
    attach: '<path d="m8 13 7-7a3 3 0 0 1 4 4L9 20a5 5 0 0 1-7-7L13 2M6 15l9-9"/>',
    smile: '<circle cx="12" cy="12" r="10"/><path d="M8 14a4 4 0 0 0 8 0M8 8h.01M16 8h.01"/>',
    send: '<path d="m3 3 19 9-19 9 4-9zM7 12h15"/>',
    lock: '<rect x="5" y="10" width="14" height="12" rx="2"/><path d="M8 10V6a4 4 0 0 1 8 0v4M12 15v3"/>',
    bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4"/>',
    pin: '<path d="m16 3 5 5-4 2-3 6-6-6 6-3zM3 21l8-8"/>',
    info: '<circle cx="12" cy="12" r="10"/><path d="M12 11v6M12 7h.01"/>',
    download: '<path d="M12 2v14m-5-5 5 5 5-5M3 17v5h18v-5"/>',
    transfer: '<path d="M7 3v18m-4-4 4 4 4-4M17 21V3m-4 4 4-4 4 4"/>',
    signal: '<path d="M4 20v-4M9 20v-8M14 20V8M19 20V3"/>',
};

export function icon(name) {
    return `<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${paths[name] || paths.info}</svg>`;
}

export function labelButton(button, name, label) {
    if (!button) return;
    button.innerHTML = icon(name);
    const text = document.createElement("span");
    text.className = "control-label";
    text.textContent = label;
    button.appendChild(text);
}
