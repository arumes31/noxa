// IDs survive role renames. Only roles already visible in the roster are
// offered; the server checks mentionability and membership at send time.
export function roleMentionChoices(clients, prefix = "") {
    const roles = new Map();
    for (const client of clients || []) {
        for (const role of client.roles || []) {
            if (Number.isSafeInteger(role.id) && role.id > 0 && typeof role.name === "string") roles.set(role.id, role.name);
        }
    }
    return [...roles].filter(([, name]) => name.toLowerCase().startsWith(prefix.toLowerCase()))
        .map(([id, name]) => ({ id, token: `<@&${id}>`, label: `@${name}` }));
}

export function roleMentionLabel(clients, id) {
    return roleMentionChoices(clients).find(role => String(role.id) === id)?.label || `@role:${id}`;
}

export function mentionFlags(message, uniqueID) {
    return {
        direct: !!uniqueID && Array.isArray(message.mentions) && message.mentions.includes(uniqueID),
        role: !!uniqueID && Array.isArray(message.role_mentions) && message.role_mentions.includes(uniqueID),
    };
}
