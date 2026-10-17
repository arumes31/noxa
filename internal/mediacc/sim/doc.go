// Package sim holds a deterministic, virtual-clock simulation of noXa's
// downstream video adaptation loop. Its tests drive the production GCC
// estimator (internal/mediacc/gcc) per viewer, a model of the relay's media
// pacer and keyframe-request coalescing, and modeled access links (RTT,
// jitter, bursty loss, capacity changes, joins). It has no production code.
package sim
