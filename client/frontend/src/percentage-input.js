// Keep typed percentages and their existing sliders on the same event path.
export function percentageInput(range, label = "") {
    const input = document.createElement("input");
    input.type = "number";
    input.className = "audio-percent";
    input.min = range.min; input.max = range.max; input.step = range.step || "1";
    input.value = range.value;
    if (label) input.setAttribute("aria-label", label + " (%)");
    range.addEventListener("input", () => { input.value = range.value; });
    const apply = clamp => {
        const value = input.valueAsNumber;
        const valid = Number.isFinite(value) && value >= Number(range.min) && value <= Number(range.max);
        if (!valid && !clamp) return;
        if (!Number.isFinite(value)) { input.value = range.value; return; }
        range.value = String(Math.max(Number(range.min), Math.min(Number(range.max), value)));
        input.value = range.value;
        range.dispatchEvent(new Event("input", { bubbles: true }));
        if (clamp) range.dispatchEvent(new Event("change", { bubbles: true }));
    };
    input.addEventListener("input", () => apply(false));
    input.addEventListener("change", () => apply(true));
    return input;
}
