// icons.js — inline outline icons (24-unit grid, 1.7 stroke).
const paths = {
    home: "M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z",
    servers: "M4 5h16v5H4zM4 14h16v5H4zM7 7.5h.01M7 16.5h.01",
    voice: "M11 5L6 9H3v6h3l5 4zM15.5 8.5a5 5 0 0 1 0 7M18.5 5.5a9 9 0 0 1 0 13",
    you: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 21a8 8 0 0 1 16 0",
    back: "M15 18l-6-6 6-6",
    send: "M4 12l16-8-6 16-2-6z",
    passkey: "M7 14a4 4 0 1 1 3.9-5H21v3h-2v3h-3v-3h-5.1A4 4 0 0 1 7 14z",
    attach: "M21 11.5l-8.5 8.5a5 5 0 0 1-7-7L14 4.5a3.3 3.3 0 0 1 4.7 4.7L10.2 17.7a1.7 1.7 0 0 1-2.4-2.4L15.5 7.6",
    mic: "M12 3a3 3 0 0 1 3 3v6a3 3 0 0 1-6 0V6a3 3 0 0 1 3-3zM5 11a7 7 0 0 0 14 0M12 18v3",
    micOff: "M3 3l18 18M9 9v3a3 3 0 0 0 5.1 2.1M15 9.3V6a3 3 0 0 0-5.9-.8M5 11a7 7 0 0 0 11.8 5.1M19 11a7 7 0 0 1-.4 2.3M12 18v3",
    hangup: "M3 15c5-5 13-5 18 0l-2.5 2.5-3-1.5v-2.5a10 10 0 0 0-7 0V16l-3 1.5z",
    screen: "M3 5h18v11H3zM8 20h8M12 16v4",
    phone: "M5 4h4l2 5-2.5 1.5a11 11 0 0 0 5 5L15 13l5 2v4a2 2 0 0 1-2 2A16 16 0 0 1 3 6a2 2 0 0 1 2-2",
    camera: "M3 7h12v10H3zM15 10l6-3v10l-6-3",
    lock: "M6 11h12v10H6zM8.5 11V7.5a3.5 3.5 0 0 1 7 0V11",
    link: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
    more: "M5 10.8a1.2 1.2 0 1 0 0 2.4 1.2 1.2 0 1 0 0-2.4zM12 10.8a1.2 1.2 0 1 0 0 2.4 1.2 1.2 0 1 0 0-2.4zM19 10.8a1.2 1.2 0 1 0 0 2.4 1.2 1.2 0 1 0 0-2.4z",
    shield: "M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z",
    plus: "M12 5v14M5 12h14",
    up: "M6 15l6-6 6 6",
    down: "M6 9l6 6 6-6",
    chevron: "M9 6l6 6-6 6",
};

export function icon(name) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("class", "icon");
    svg.setAttribute("width", "20");
    svg.setAttribute("height", "20");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.7");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    svg.setAttribute("aria-hidden", "true");
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", paths[name]);
    svg.append(path);
    return svg;
}
