
## 2026-10-15 - Zero-Allocation UDP Reads
**Learning:** The `internal/server/udp.go` server used a `sync.Pool` for reading, but accidentally copied `[]byte` payloads inside the tight `readLoop` (doing `pkt := make([]byte, n); copy(...)`) before sending to workers via channels. This defeated the purpose of the pool for memory savings per packet. We can pass the pooled buffer to the channel directly and rely on `defer` in the worker to return the slice to the pool.
**Action:** When working with Go network servers in this codebase (or others), check whether the `readLoop` makes redundant `make([]byte)` allocations when it could transfer ownership of pooled buffers instead.
