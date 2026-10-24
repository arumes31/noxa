// Starts one real server for the whole run. Worker restarts after a failed
// test reuse it instead of provisioning the database again.
import { startServer } from "./harness.mjs";

export default async function globalSetup() {
    const server = await startServer();
    process.env.NOXA_E2E_SERVER_STATE = JSON.stringify({ webURL: server.webURL, accounts: server.accounts, controlAddr: server.controlAddr });
    return async () => server.stop();
}
