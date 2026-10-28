// Worker entry for encrypted channel media. The maintained LiveKit worker is
// loaded first (it installs its own handlers); installMediaE2EEWorker then
// replaces every page-reachable entry point with the noXa wrapper.
import "livekit-client/e2ee-worker";
import { installMediaE2EEWorker } from "./media-e2ee-core.js";

installMediaE2EEWorker(self);
