//go:build race

package webrtc

// chaosRace relaxes wall-clock assertions of the real-transport chaos test:
// the race detector multiplies Pion and router costs on a small runner.
const chaosRace = true
