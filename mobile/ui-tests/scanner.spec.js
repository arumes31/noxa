import { test, expect } from "@playwright/test";
const url = "http://127.0.0.1:5175/app/ui-tests/scanner.html";
async function camera(page, image = "pairing") {
    await page.addInitScript(image => {
        window.cameraRequests = 0;
        Object.defineProperty(navigator.mediaDevices, "getUserMedia", { value: async constraints => {
            window.cameraRequests++;
            if (constraints.audio !== false) throw new Error("audio requested");
            const canvas = document.createElement("canvas");
            canvas.width = canvas.height = 640;
            const ctx = canvas.getContext("2d");
            const picture = new Image();
            picture.src = `/app/ui-tests/fixtures/${image}.png`;
            await picture.decode();
            ctx.imageSmoothingEnabled = false;
            ctx.fillStyle = "white"; ctx.fillRect(0, 0, 640, 640);
            ctx.drawImage(picture, 0, 0, 640, 640);
            window.cameraStream = canvas.captureStream(10);
            return window.cameraStream;
        } });
    }, image);
}
test("QR pixels are decoded and camera stops before handing off the link", async ({ page }) => {
    await camera(page);
    await page.goto(url);
    expect(await page.evaluate(() => window.cameraRequests)).toBe(0);
    await page.evaluate(() => {
        window.scanDone = false;
        import("/app/src/ui/qr-scanner.js").then(({ scanPairingQR }) => scanPairingQR()).then(result => {
            window.scanResult = { id: result?.linkID, length: result?.secret.length };
            window.scanDone = true;
        });
    });
    await expect.poll(() => page.evaluate(() => window.scanDone)).toBe(true);
    expect(await page.evaluate(() => window.scanResult)).toEqual({ id: "test-link", length: 32 });
    expect(await page.evaluate(() => window.cameraStream.getTracks().every(t => t.readyState === "ended"))).toBe(true);
    await expect(page.getByRole("dialog")).toHaveCount(0);
});
test("foreign server QR stays in scanner; cancel releases camera", async ({ page }) => {
    await camera(page, "foreign");
    await page.goto(url);
    await page.getByRole("button", { name: "Scan QR code" }).click();
    await expect(page.getByRole("status")).toHaveText("Use a login QR code for this server.");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.cameraStream.getTracks().every(t => t.readyState === "ended"))).toBe(true);
    await expect(page.getByLabel("Code from your desktop")).toBeEnabled();
});
test("camera denial leaves code entry available", async ({ page }) => {
    await page.addInitScript(() => Object.defineProperty(navigator.mediaDevices, "getUserMedia", { value: async () => { throw new DOMException("Denied", "NotAllowedError"); } }));
    await page.goto(url);
    await page.getByRole("button", { name: "Scan QR code" }).click();
    await expect(page.getByRole("status")).toContainText("Camera unavailable or access denied");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(page.getByLabel("Code from your desktop")).toBeEnabled();
});
test("late permission grant after cancel stops its stream", async ({ page }) => {
    await page.addInitScript(() => Object.defineProperty(navigator.mediaDevices, "getUserMedia", { value: () => new Promise(resolve => { window.grant = () => { window.stopped = false; resolve({ getTracks: () => [{ stop: () => { window.stopped = true; } }] }); }; }) }));
    await page.goto(url);
    await page.getByRole("button", { name: "Scan QR code" }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.evaluate(() => window.grant());
    await expect.poll(() => page.evaluate(() => window.stopped)).toBe(true);
});
test("QR parser rejects credentials, foreign origins and invalid secrets", async ({ page }) => {
    await page.goto(url);
    const accepted = await page.evaluate(async () => {
        const { pairingLinkFromQR } = await import("/app/src/ui/qr-scanner.js");
        const fragment = "#id=test-link&k=" + "A".repeat(43);
        return ["https://evil.example/link", "javascript:alert(1)", location.origin + "/app/", location.origin + "/link?redirect=1", "http://user@127.0.0.1:5175/link"].map(u => !!pairingLinkFromQR(u + fragment))
            .concat(!!pairingLinkFromQR(location.origin + "/link#id=x&k=abc"));
    });
    expect(accepted).toEqual([false, false, false, false, false, false]);
});

test("route cleanup stops scanner after a replacement sign-in screen", async ({ page }) => {
    await camera(page, "foreign");
    await page.goto(url);
    await page.evaluate(async () => {
        const { renderConnect } = await import("/app/src/ui/connect.js");
        const root = document.getElementById("app");
        window.routeCleanup = renderConnect(root, { onSignedIn() {} });
        renderConnect(root, { onSignedIn() {} });
    });
    await page.getByRole("button", { name: "Scan QR code" }).click();
    await expect(page.getByRole("status")).toHaveText("Use a login QR code for this server.");
    await page.evaluate(() => window.routeCleanup());
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(await page.evaluate(() => window.cameraStream.getTracks().every(t => t.readyState === "ended"))).toBe(true);
});
