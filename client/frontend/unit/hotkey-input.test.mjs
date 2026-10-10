import { test } from 'node:test';
import assert from 'node:assert/strict';
import { capturedHotkey, hotkeyLabel } from '../src/hotkey-input.js';

test('navigation browser names become native hotkey names', () => {
    for (const [key, want] of [['PageUp', 'PageUp'], ['PageDown', 'PageDown'], ['Home', 'Home'], ['End', 'End'], ['ArrowUp', 'Up'], ['ArrowDown', 'Down'], ['Insert', 'Insert'], ['Backspace', 'Backspace']]) {
        assert.equal(capturedHotkey({ key, ctrlKey: true }), `Ctrl+${want}`);
    }
});
test('numpad digits and arithmetic stay distinct from the main keyboard', () => {
    for (let i = 0; i < 10; i++) {
        assert.equal(capturedHotkey({ key: String(i), code: `Numpad${i}`, location: 3 }), `Numpad${i}`);
        assert.equal(capturedHotkey({ key: String(i), code: `Digit${i}` }), String(i));
    }
    for (const [code, key] of [['NumpadAdd', '+'], ['NumpadSubtract', '-'], ['NumpadMultiply', '*'], ['NumpadDivide', '/'], ['NumpadDecimal', ',']]) {
        assert.equal(capturedHotkey({ key, code, location: 3, altKey: true }), `Alt+${code}`);
    }
    assert.equal(capturedHotkey({ key: 'End', code: 'Numpad1', location: 3 }), 'End');
    assert.equal(capturedHotkey({ key: 'Enter', code: 'NumpadEnter', location: 3 }), 'Enter');
});
test('DE and EN punctuation positions capture even dead keys and AltGr', () => {
    const positions = {
        Backquote: ['Dead', '`'], Minus: ['ß', '-'], Equal: ['Dead', '='],
        BracketLeft: ['ü', '['], BracketRight: ['+', ']'], Backslash: ['#', '\\'],
        Semicolon: ['ö', ';'], Quote: ['ä', "'"], IntlBackslash: ['<', '\\'],
        Comma: [',', '<'], Period: ['.', '>'], Slash: ['-', '/'],
    };
    for (const [code, keys] of Object.entries(positions)) {
        for (const key of keys) assert.equal(capturedHotkey({ key, code, ctrlKey: true }), `Ctrl+${code}`);
    }
    assert.equal(capturedHotkey({ key: '€', code: 'KeyE', ctrlKey: true, altKey: true }), 'Ctrl+Alt+E');
    assert.equal(capturedHotkey({ key: '!', code: 'Digit1', shiftKey: true }), 'Shift+1');
    assert.equal(capturedHotkey({ key: 'y', code: 'KeyZ' }), 'Y');
});
test('modifier-only, composition and unsupported keys do not become bindings', () => {
    for (const key of ['Control', 'Alt', 'AltGraph', 'Shift', 'Meta']) assert.equal(capturedHotkey({ key }), null);
    assert.equal(capturedHotkey({ key: 'a', isComposing: true }), null);
    for (const key of ['Unidentified', 'Fn', 'Dead', 'Banana']) assert.equal(capturedHotkey({ key }), '');
    for (const key of ['F24', 'CapsLock', 'NumLock', 'ScrollLock', 'Pause', 'PrintScreen', 'ContextMenu', 'AudioVolumeMute', 'MediaPlayPause']) assert.equal(capturedHotkey({ key }), key);
    assert.equal(capturedHotkey({ key: ' ', metaKey: true }), 'Win+Space');
});
test('readable labels distinguish numpad and show DE/EN punctuation', () => {
    assert.equal(hotkeyLabel('Ctrl+PageDown', 'de'), 'Ctrl+Bild ↓');
    assert.equal(hotkeyLabel('Ctrl+NumpadAdd', 'de'), 'Ctrl+Num +');
    assert.equal(hotkeyLabel('Ctrl+BracketRight', 'de'), 'Ctrl++ / ]');
    assert.equal(hotkeyLabel('Ctrl+Semicolon', 'en'), 'Ctrl+Ö / ;');
    assert.equal(hotkeyLabel('Shift+A', 'en'), 'Shift+A');
});
