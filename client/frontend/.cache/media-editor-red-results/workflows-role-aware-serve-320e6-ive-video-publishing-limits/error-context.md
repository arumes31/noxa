# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: workflows.spec.js >> role-aware server configuration >> delegated managers can edit live video publishing limits
- Location: tests\workflows.spec.js:3484:5

# Error details

```
Error: expect(locator).toHaveValue(expected) failed

Locator: getByLabel('Video bitrate ceiling (bit/s, 0 = unlimited)')
Expected: "800000"
Timeout: 5000ms
Error: element(s) not found

Call log:
  - Expect "toHaveValue" with timeout 5000ms
  - waiting for getByLabel('Video bitrate ceiling (bit/s, 0 = unlimited)')

```

```yaml
- dialog "noXa":
  - heading "noXa" [level=1]
  - text: SERVER
  - textbox "SERVER": 127.0.0.1:12333
  - text: NICKNAME
  - textbox "NICKNAME":
    - /placeholder: Nickname
  - text: Server password (optional)
  - textbox "Server password (optional)":
    - /placeholder: If required
  - button "CONNECT"
  - alert
  - text: RECENT SERVERS
  - region "Recent servers": no recent servers
  - text: Identity auto-generated — stored locally playwright-i… noXa test
- region "Notifications"
- dialog "Settings":
  - heading "Settings" [level=2]
  - tablist "Settings sections":
    - tab "Application"
    - tab "Capture"
    - tab "Playback"
    - tab "Hotkeys"
    - tab "Whisper"
    - tab "Downloads"
    - tab "Chat"
    - tab "Security"
    - tab "Server" [selected]
    - tab "Notifications"
  - text: Search settings
  - textbox "Search settings":
    - /placeholder: search settings…
  - tabpanel "Server":
    - status: Changes take effect immediately and are restored after restart.
    - text: Maximum clients (0 = unlimited)
    - spinbutton "Maximum clients (0 = unlimited)": "100"
    - text: Connection timeout (seconds)
    - spinbutton "Connection timeout (seconds)": "90"
    - text: Default Opus bitrate (bit/s)
    - spinbutton "Default Opus bitrate (bit/s)": "64000"
    - text: Default Opus in-band FEC
    - checkbox "Default Opus in-band FEC" [checked]
    - text: Default Opus DTX
    - checkbox "Default Opus DTX"
    - text: Default Opus stereo
    - checkbox "Default Opus stereo" [checked]
    - text: Codec defaults apply to newly created channels. Existing channels keep their explicit settings. Runtime settings
    - button "Apply server configuration"
  - button "OK"
  - button "Cancel"
  - button "Apply"
```

# Test source

```ts
  3389 |                 await expect(page.getByRole("dialog").getByRole("status")).toHaveText("Removal could not be confirmed. Reload bans before trying again.");
  3390 |                 await expect(page.locator(control)).toBeDisabled();
  3391 |                 await expect(page.getByRole("button", { name: "Reload bans", exact: true })).toBeEnabled();
  3392 |             } else await expect.poll(() => page.evaluate(() => window.__management.toasts.length)).toBe(1);
  3393 |             expect(await page.evaluate(() => window.__management.effects)).toEqual([]);
  3394 |             expect(await page.evaluate(() => window.__management.calls.every(c => c.tabID === "server-a"))).toBe(true);
  3395 |         });
  3396 |     }
  3397 | 
  3398 |     test("legacy audit pagination stays on the opening server", async ({ page }) => {
  3399 |         await page.evaluate(() => window.__noxaPerms.openAuditViewer());
  3400 |         await expect(page.locator(".audit-grid")).toContainText("ban_remove");
  3401 |         await page.evaluate(() => { window.__management.nativeTab = "server-b"; });
  3402 |         await page.locator(".audit-older").click();
  3403 |         await expect.poll(() => page.evaluate(() => window.__management.toasts.length)).toBe(1);
  3404 |         expect(await page.evaluate(() => window.__management.calls.at(-1))).toEqual({ name: "AuditLog", tabID: "server-a", args: [7, 50] });
  3405 |     });
  3406 | 
  3407 |     for (const [open, error] of [["openBanList", "ban list failed"], ["openComplaints", "complaint list failed"], ["openChatFilters", "loading filters failed"]]) {
  3408 |         test(`${open} honors server denial despite stale administrator flag`, async ({ page }) => {
  3409 |             await page.evaluate(name => {
  3410 |                 window.__noxa.state.isAdmin = true;
  3411 |                 window.__management.denied = true;
  3412 |                 window.__noxaPerms[name]();
  3413 |             }, open);
  3414 |             await expect(page.getByRole("dialog")).toContainText(error);
  3415 |             expect(await page.evaluate(() => window.__management.effects)).toEqual([]);
  3416 |             if (open === "openChatFilters") await expect(page.locator(".cf-save")).toBeDisabled();
  3417 |         });
  3418 |     }
  3419 | });
  3420 | 
  3421 | test.describe("role-aware server configuration", () => {
  3422 |     test.beforeEach(async ({ page }) => {
  3423 |         await page.evaluate(() => {
  3424 |             const v = window.__noxa;
  3425 |             v.state.authorizationModel = "roles-v1";
  3426 |             v.state.isAdmin = false;
  3427 |             v.state.activeTabID = "server-a";
  3428 |             window.__serverSettings = { loads: 0, saves: [], mediaLoads: 0, mediaSaves: [], toasts: [], config: {
  3429 |                 max_clients: 100, client_timeout_seconds: 90, opus_bitrate: 64000,
  3430 |                 opus_fec: true, opus_dtx: false, opus_stereo: true,
  3431 |             }, media: { video_max_bitrate: 800000, video_max_width: 1280, video_max_height: 720 } };
  3432 |             v.toast = message => window.__serverSettings.toasts.push(message);
  3433 |             const app = window.go.main.App;
  3434 |             window.go.main.App = new Proxy(app, { get(target, key) {
  3435 |                 if (key === "GetServerConfigForTab") return async tabID => {
  3436 |                     if (tabID !== "server-a") throw new Error("wrong server tab");
  3437 |                     const fixture = window.__serverSettings;
  3438 |                     fixture.loads++;
  3439 |                     if (fixture.delayLoad) await new Promise(resolve => { fixture.finishLoad = resolve; });
  3440 |                     if (fixture.denied) throw new Error("permission denied");
  3441 |                     return structuredClone(fixture.config);
  3442 |                 };
  3443 |                 if (key === "SetServerConfigForTab") return async (tabID, config) => {
  3444 |                     if (tabID !== "server-a") throw new Error("wrong server tab");
  3445 |                     const fixture = window.__serverSettings;
  3446 |                     fixture.saves.push(structuredClone(config));
  3447 |                     if (fixture.delaySave) await new Promise(resolve => { fixture.finishSave = resolve; });
  3448 |                     if (fixture.denied) throw new Error("permission denied");
  3449 |                     fixture.config = structuredClone(config);
  3450 |                     return structuredClone(config);
  3451 |                 };
  3452 |                 if (key === "GetMediaLimitsForTab") return async tabID => {
  3453 |                     if (tabID !== "server-a") throw new Error("wrong server tab");
  3454 |                     const fixture = window.__serverSettings;
  3455 |                     fixture.mediaLoads++;
  3456 |                     if (fixture.denied) throw new Error("permission denied");
  3457 |                     return structuredClone(fixture.media);
  3458 |                 };
  3459 |                 if (key === "SetMediaLimitsForTab") return async (tabID, limits) => {
  3460 |                     if (tabID !== "server-a") throw new Error("wrong server tab");
  3461 |                     const fixture = window.__serverSettings;
  3462 |                     fixture.mediaSaves.push(structuredClone(limits));
  3463 |                     if (fixture.delaySave) await new Promise(resolve => { fixture.finishSave = resolve; });
  3464 |                     if (fixture.denied) throw new Error("permission denied");
  3465 |                     fixture.media = structuredClone(limits);
  3466 |                     return { revision: "2", ...structuredClone(limits) };
  3467 |                 };
  3468 |                 return target[key];
  3469 |             } });
  3470 |         });
  3471 |     });
  3472 | 
  3473 |     test("delegated managers can save without the legacy administrator flag", async ({ page }) => {
  3474 |         await page.evaluate(() => window.__noxa.openSettings("server"));
  3475 |         await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
  3476 |         await page.getByLabel("Maximum clients (0 = unlimited)").fill("75");
  3477 |         await page.getByRole("button", { name: "Apply server configuration", exact: true }).click();
  3478 |         await expect.poll(() => page.evaluate(() => window.__serverSettings.saves)).toEqual([{
  3479 |             max_clients: 75, client_timeout_seconds: 90, opus_bitrate: 64000,
  3480 |             opus_fec: true, opus_dtx: false, opus_stereo: true,
  3481 |         }]);
  3482 |     });
  3483 | 
  3484 |     test("delegated managers can edit live video publishing limits", async ({ page }) => {
  3485 |         await page.evaluate(() => window.__noxa.openSettings("server"));
  3486 |         const rate = page.getByLabel("Video bitrate ceiling (bit/s, 0 = unlimited)");
  3487 |         const width = page.getByLabel("Maximum encoded video width (0 = unlimited)");
  3488 |         const height = page.getByLabel("Maximum encoded video height (0 = unlimited)");
> 3489 |         await expect(rate).toHaveValue("800000");
       |                            ^ Error: expect(locator).toHaveValue(expected) failed
  3490 |         await expect(width).toHaveValue("1280");
  3491 |         await expect(height).toHaveValue("720");
  3492 |         await rate.fill("600000");
  3493 |         await width.fill("640");
  3494 |         await height.fill("360");
  3495 |         await page.getByRole("button", { name: "Apply video publishing limits", exact: true }).click();
  3496 |         await expect.poll(() => page.evaluate(() => window.__serverSettings.mediaSaves)).toEqual([{
  3497 |             video_max_bitrate: 600000, video_max_width: 640, video_max_height: 360,
  3498 |         }]);
  3499 |         await width.fill("640");
  3500 |         await height.fill("0");
  3501 |         await page.getByRole("button", { name: "Apply video publishing limits", exact: true }).click();
  3502 |         expect(await page.evaluate(() => window.__serverSettings.mediaSaves.length)).toBe(1);
  3503 |         await expect(height).toHaveJSProperty("validationMessage", "Set both dimensions to 0, or set both between 1 and 16383.");
  3504 |     });
  3505 | 
  3506 |     test("server authorization works before model discovery and denies legacy non-admins", async ({ page }) => {
  3507 |         await page.evaluate(() => { delete window.__noxa.state.authorizationModel; window.__noxa.openSettings("server"); });
  3508 |         await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
  3509 |         await page.evaluate(() => { window.__serverSettings.denied = true; window.__noxa.openSettings("server"); });
  3510 |         await expect(page.locator("#settings-content")).toContainText("permission denied");
  3511 |         await expect(page.getByRole("button", { name: "Apply server configuration", exact: true })).toBeHidden();
  3512 |     });
  3513 | 
  3514 |     test("role-mode denials override a stale legacy administrator flag", async ({ page }) => {
  3515 |         await page.evaluate(() => {
  3516 |             window.__noxa.state.isAdmin = true;
  3517 |             window.__serverSettings.denied = true;
  3518 |             window.__noxa.openSettings("server");
  3519 |         });
  3520 |         await expect(page.locator("#settings-content")).toContainText("permission denied");
  3521 |         await expect(page.getByRole("button", { name: "Apply server configuration", exact: true })).toBeHidden();
  3522 |         expect(await page.evaluate(() => window.__serverSettings.loads)).toBe(1);
  3523 |     });
  3524 | 
  3525 |     test("invalid numbers and duplicate submissions never reach the backend", async ({ page }) => {
  3526 |         await page.evaluate(() => { window.__serverSettings.delaySave = true; window.__noxa.openSettings("server"); });
  3527 |         const maximum = page.getByLabel("Maximum clients (0 = unlimited)");
  3528 |         await maximum.fill("-1");
  3529 |         const apply = page.getByRole("button", { name: "Apply server configuration", exact: true });
  3530 |         await apply.click();
  3531 |         expect(await page.evaluate(() => window.__serverSettings.saves)).toEqual([]);
  3532 |         await maximum.fill("50");
  3533 |         await apply.click();
  3534 |         await expect(apply).toBeDisabled();
  3535 |         await expect(maximum).toBeDisabled();
  3536 |         await page.evaluate(() => window.__serverSettings.finishSave());
  3537 |         await expect(apply).toBeEnabled();
  3538 |         expect(await page.evaluate(() => window.__serverSettings.saves.length)).toBe(1);
  3539 |     });
  3540 | 
  3541 |     test("failed saves require an explicit reload before retry", async ({ page }) => {
  3542 |         await page.evaluate(() => window.__noxa.openSettings("server"));
  3543 |         const apply = page.getByRole("button", { name: "Apply server configuration", exact: true });
  3544 |         await expect(apply).toBeVisible();
  3545 |         await page.evaluate(() => { window.__serverSettings.denied = true; });
  3546 |         await apply.click();
  3547 |         await expect(apply).toBeDisabled();
  3548 |         await expect(page.locator("#settings-content")).toContainText("permission denied");
  3549 |         await page.evaluate(() => { window.__serverSettings.denied = false; window.__serverSettings.config.max_clients = 12; });
  3550 |         await page.getByRole("button", { name: "Reload server configuration", exact: true }).click();
  3551 |         await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("12");
  3552 |         await expect(apply).toBeEnabled();
  3553 |         expect(await page.evaluate(() => window.__serverSettings.saves.length)).toBe(1);
  3554 |     });
  3555 | 
  3556 |     test("a server switch prevents stale saves and ignores late replies", async ({ page }) => {
  3557 |         await page.evaluate(() => { window.__serverSettings.delayLoad = true; window.__noxa.openSettings("server"); });
  3558 |         await expect.poll(() => page.evaluate(() => typeof window.__serverSettings.finishLoad)).toBe("function");
  3559 |         await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__serverSettings.finishLoad(); });
  3560 |         await expect(page.locator("#settings-content")).toContainText("The server changed");
  3561 |         await expect(page.getByRole("button", { name: "Apply server configuration", exact: true })).toBeHidden();
  3562 |         await page.evaluate(() => { window.__serverSettings.delayLoad = false; });
  3563 |         await page.getByRole("button", { name: "Reload server configuration", exact: true }).click();
  3564 |         await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
  3565 |         await page.evaluate(() => { window.__noxa.state.serverGeneration++; });
  3566 |         await page.getByRole("button", { name: "Apply server configuration", exact: true }).click();
  3567 |         expect(await page.evaluate(() => window.__serverSettings.saves)).toEqual([]);
  3568 |     });
  3569 | 
  3570 |     test("a save reply from the previous server cannot display success on the next", async ({ page }) => {
  3571 |         await page.evaluate(() => { window.__serverSettings.delaySave = true; window.__noxa.openSettings("server"); });
  3572 |         await page.getByRole("button", { name: "Apply server configuration", exact: true }).click();
  3573 |         await expect.poll(() => page.evaluate(() => typeof window.__serverSettings.finishSave)).toBe("function");
  3574 |         await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__serverSettings.finishSave(); });
  3575 |         await expect(page.locator("#settings-content")).toContainText("The server changed");
  3576 |         expect(await page.evaluate(() => window.__serverSettings.toasts)).toEqual([]);
  3577 |     });
  3578 | 
  3579 |     test("settings search indexes server labels without requesting protected data", async ({ page }) => {
  3580 |         await page.evaluate(() => window.__noxa.openSettings("application"));
  3581 |         await page.locator("#settings-search").fill("Maximum clients");
  3582 |         await expect(page.locator(".set-search-hit")).toHaveCount(1);
  3583 |         expect(await page.evaluate(() => window.__serverSettings.loads)).toBe(0);
  3584 |         await page.locator(".set-search-hit").click();
  3585 |         await expect(page.getByLabel("Maximum clients (0 = unlimited)")).toHaveValue("100");
  3586 |     });
  3587 | });
  3588 | 
  3589 | test("role cosmetics use visible snapshot members, highest hoist and safe inline icons", async ({ page }) => {
```