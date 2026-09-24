// Password-authenticated account IDs can differ from the local identity used
// to scope device-side DM storage. Wire membership uses the session roster ID.
export function sessionUserID(state) {
    return (state.myClientID && state.clients?.find(client => client.client_id === state.myClientID)?.unique_id)
        || state.myUniqueID || "";
}
