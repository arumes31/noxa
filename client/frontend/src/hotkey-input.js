// Punctuation uses physical codes: the same position can be Ö or ;, or a
// dead key. Canonical names avoid ambiguous specs such as Ctrl++.
const punctuation = {
    Backquote: '^ / `', Minus: 'ß / −', Equal: '´ / =', BracketLeft: 'Ü / [',
    BracketRight: '+ / ]', Backslash: '# / \\', Semicolon: 'Ö / ;', Quote: 'Ä / \u0027',
    IntlBackslash: '< / >', Comma: ',', Period: '.', Slash: '− / /',
};
const names = new Set(['PageUp', 'PageDown', 'Home', 'End', 'Insert', 'Backspace',
    'Tab', 'Enter', 'Escape', 'Delete', 'CapsLock', 'NumLock', 'ScrollLock',
    'Pause', 'PrintScreen', 'ContextMenu', 'AudioVolumeUp', 'AudioVolumeDown',
    'AudioVolumeMute', 'MediaPlayPause', 'MediaTrackNext', 'MediaTrackPrevious', 'MediaStop']);
const numpadOperators = new Set(['NumpadAdd', 'NumpadSubtract', 'NumpadMultiply', 'NumpadDivide', 'NumpadDecimal']);
const arrows = { ArrowUp: 'Up', ArrowDown: 'Down', ArrowLeft: 'Left', ArrowRight: 'Right' };

// null waits for a main key; an empty string reports an unavailable key.
export function capturedHotkey(e) {
    if (e.isComposing || ['Control', 'Alt', 'AltGraph', 'Shift', 'Meta'].includes(e.key)) return null;
    let key;
    if (/^Numpad[0-9]$/.test(e.code || '') && /^[0-9]$/.test(e.key)) key = e.code;
    else if (numpadOperators.has(e.code) && !['Delete', 'Insert'].includes(e.key)) key = e.code;
    else if (Object.hasOwn(punctuation, e.code)) key = e.code;
    else if (arrows[e.key]) key = arrows[e.key];
    else if (names.has(e.key) || /^F([1-9]|1[0-9]|2[0-4])$/.test(e.key)) key = e.key;
    else if (e.key === ' ') key = 'Space';
    else if (/^[a-z]$/i.test(e.key)) key = e.key.toUpperCase();
    else if (/^Key[A-Z]$/.test(e.code || '')) key = e.code.slice(3); // AltGr symbols
    else if (/^Digit[0-9]$/.test(e.code || '')) key = e.code.slice(5);
    else if (/^[0-9]$/.test(e.key)) key = e.key;
    else return '';
    const parts = [];
    if (e.ctrlKey) parts.push('Ctrl');
    if (e.altKey) parts.push('Alt');
    if (e.shiftKey) parts.push('Shift');
    if (e.metaKey) parts.push('Win');
    return [...parts, key].join('+');
}

export function hotkeyLabel(spec, language) {
    const de = language === 'de';
    const labels = {
        ...punctuation, NumpadAdd: 'Num +', NumpadSubtract: 'Num −', NumpadMultiply: 'Num ×',
        NumpadDivide: 'Num ÷', NumpadDecimal: 'Num . / ,',
        ...(de ? { PageUp: 'Bild ↑', PageDown: 'Bild ↓', Home: 'Pos1', End: 'Ende',
            Insert: 'Einfg', Delete: 'Entf', Backspace: 'Rücktaste', Space: 'Leertaste' } : {}),
    };
    return spec.split('+').map(part => labels[part] || (/^Numpad[0-9]$/.test(part) ? `Num ${part.slice(-1)}` : part)).join('+');
}
