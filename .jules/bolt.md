## 2024-11-20 - WebRTC Gap Flushing Optimization
**Learning:** The hot path for WebRTC gap recovery in `video_reorder.go` allocated a slice for tracking pending deadlines on every missing packet, which increased GC pressure unnecessarily.
**Action:** Always consider retaining and reusing slices in structs on WebRTC hot paths when the size is bounded, and use Go 1.21 generic `slices.SortFunc` over reflection-based `sort.Slice` to eliminate interface allocation.
