// Public video API. Rendering, publication and negotiation own separate lifecycles.
import { createVideoGrid } from "./video-grid.js";
import { createVideoPublication } from "./video-publication.js";

export { cameraConstraints } from "./camera-capture.js";
export { parseTrackID, SLOT_SCREEN_AUDIO } from "./media-track-id.js";
export { trackSlots, queuePeerNegotiation, answerRemoteOffer, renegotiate } from "./video-negotiation.js";

let publication;
const grid = createVideoGrid({
    applySendCaps: (...args) => publication.applySendCaps(...args),
    syncCameraButton: () => publication.syncCameraButton(),
    syncShareButton: () => publication.syncShareButton(),
});
publication = createVideoPublication({ policy: grid });

export const { videoTrackAdded, videoTrackRemoved, videoSpeaking, videoRefreshNames, clearVideoGrid, initVideo, setIdleQualityOverride, isLowBandwidth, setLowBandwidth } = grid;
export const { applyVideoLimits, shareToggle, clearRegionBox, resetCameraState, cameraToggle } = publication;
