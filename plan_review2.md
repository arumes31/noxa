The E2E test `tab-bound custom emoji > late emoji replies cannot attach the previous message to a replacement reaction strip` is failing with a timeout waiting for `window.__emoji.completed.includes("EmojiList")` to become true.
This wait happens at `client/frontend/tests/workflows.spec.js:3154`.

In my changes to `applyCustomEmoji`, I added:
```javascript
function applyCustomEmoji(html) {
    if (typeof html !== "string" || html.indexOf(":") === -1) return html;
    if (customDirty) ensureCustomEmoji();
```
Previously it was:
```javascript
function applyCustomEmoji(html) {
    if (customDirty) ensureCustomEmoji();
    for (const e of customEmoji) {
```

So `ensureCustomEmoji()` is NO LONGER called unconditionally on every message render (unless the message has a `:`).
If a message is rendered but it doesn't contain a colon (`:`), `applyCustomEmoji` returns early.
This means `ensureCustomEmoji()` doesn't get called.
In the E2E test, `EmojiListForTab` is mocked to track calls. The test expects `EmojiListForTab` (which translates to `EmojiList` in the backend mock) to be completed when a message is rendered! Wait, when does the test expect it?
The test says:
```javascript
        await expect.poll(() => page.evaluate(() => window.__emoji.completed.includes("EmojiList"))).toBe(true);
        await page.locator('.msg[data-msg-id="1"]').hover();
        await page.locator('.msg[data-msg-id="1"]').getByRole("button", { name: "react", exact: true }).click();
        await expect.poll(() => page.evaluate(() => window.__emoji.calls.some(c => c.name === "EmojiGet" && c.args[0] === "wave"))).toBe(true);
```
The reaction picker might trigger `ensureCustomEmoji` too, but maybe it expects the initial render to have triggered it?
Wait, if `customDirty` is true and a message is rendered without a colon, `ensureCustomEmoji()` is not called until either:
1) A message *with* a colon is rendered.
2) Something else calls `ensureCustomEmoji()`, like the reaction strip `ensureCustomEmoji().then(...)` or the emoji picker.

Let's check `ensureCustomEmoji` calls.
It's called in:
- `applyCustomEmoji`
- `buildReactionStrip` -> `ensureCustomEmoji().then(...)`
- `buildEmojiPicker` -> `ensureCustomEmoji().then(...)`

In the test, we hover over a message and click the react button. Wait, if the test is waiting for `EmojiList` *before* hovering over the message:
`await expect.poll(() => page.evaluate(() => window.__emoji.completed.includes("EmojiList"))).toBe(true);`
It times out here! It expects `EmojiList` to have happened just because a message was rendered.
Why did it happen before? Because *any* message rendering called `applyCustomEmoji`, which called `ensureCustomEmoji`.
Now, only messages with `:` call `ensureCustomEmoji`.

To fix this, maybe we should just call `ensureCustomEmoji()` when a tab is selected or a channel is loaded, OR we should restore the `if (customDirty) ensureCustomEmoji();` to run *before* the early return in `applyCustomEmoji()`.

If we move `if (customDirty) ensureCustomEmoji();` back to the top of `applyCustomEmoji`, it will trigger the network call eagerly (as it used to), but it still won't iterate over the emojis array unless there's a `:` in the text. This preserves the E2E test's assumption about eager loading without compromising the CPU optimization.

Wait, `ensureCustomEmoji` fetches the custom emojis from the server. Is it good to fetch them eagerly? It probably is, because they are needed for auto-complete and the emoji picker. The main performance bottleneck wasn't the single `EmojiList` call (which sets `customDirty = false`), but the massive `for` loop executing on every single message render, and for every message triggering `emojiURL` cache checks for 1000s of emojis.

Let's look at my patch:
```javascript
function applyCustomEmoji(html) {
    if (typeof html !== "string" || html.indexOf(":") === -1) return html;
    if (customDirty) ensureCustomEmoji();
```

If I change it to:
```javascript
function applyCustomEmoji(html) {
    if (customDirty) ensureCustomEmoji();
    if (typeof html !== "string" || html.indexOf(":") === -1) return html;
```
This restores the exact same side-effect behavior (fetching the emoji list if dirty when any message renders) while keeping the optimization of skipping the large `customEmoji` loop for every message that doesn't contain a `:`.

Let's verify this theory.
