// encoder-vendor.js — display label for a reported hardware encoder vendor.
const LABELS = Object.freeze({ nvidia: "NVIDIA", amd: "AMD", intel: "Intel" });

/** " · NVIDIA" for a known vendor, "" otherwise. */
export function vendorLabel(vendor) {
    return Object.hasOwn(LABELS, vendor) ? ` · ${LABELS[vendor]}` : "";
}
