# Explicit Windows sender timing trace

The sharing-details panel offers a local, one-shot sender trace on Windows. Nothing
is collected until Start is clicked. The Go arm requires an authenticated, exact
server tab; a one-use token expires after ten seconds. Only the released Wails
page can submit the fixed start/stop/status/export commands. No debug listener,
server endpoint, browser flags, codec change or second upload is introduced.

The frontend requires one active screen sender/encoding and no camera sender on
that peer. It checks share, session, track, sender, peer and codec continuity while
active. Scope loss requests native invalidation. Stop, source replacement, page
navigation and renderer failure cannot turn an interrupted record into success.
Native callbacks and timers run on the WebView owner apartment. Pending Start is
kept alive so a late success can still be stopped. Unknown cleanup blocks another
trace in that process; it is never silently retried or adopted.

The target duration is 60 seconds. The 250-ms native timer is cooperative: a blocked
owner thread can delay Stop. Durations beyond 61.5 seconds are marked incomplete.
Start and completion have ten-second deadlines. The native record-until-full
buffer is 4 MiB; data messages, cumulative raw JSON, retained events and numeric
export are separately bounded (8 MiB, 32 MiB, 32,768 events and less than 2 MiB).
These are feasibility limits, not a guarantee of 60 seconds at every packet rate.
Buffer pressure, malformed data, missing markers, data loss and unknown completion
produce explicit incomplete results. Actual elapsed time is exported.

Only fixed encoder invocation, encoded callback, pacer enqueue and pacer process
markers survive parsing. Export retains marker kind and numeric trace timestamps,
RTP timestamps and packet sequence numbers where defined. Raw trace strings,
source titles, payloads, URLs, process/thread identifiers and arbitrary arguments
are not exported. One process across all marker kinds and one producer thread per
kind are required; this does not prove a unique stream within that process.

Copy fetches the native numeric result explicitly. One saved frontend control is
retained for up to 30 minutes. A new trace invalidates uncopied native data; an
already explicitly copied result remains immutable. Clearing the saved control or
leaving the page removes its listener and archive. The native cache holds at most
one bounded result until replacement or process exit.

`trace_us` uses the runtime trace clock, not UTC. This first version has no clock
bracket to ordinary getStats samples. Compare cadence within the trace only; do
not align it to wall-clock BWE events or infer network transmission time. Encode
invocation is not completion; an encoded callback can include scheduling delay.
Pacer enqueue is inside a posted task and is not queue residence. Existing markers
do not expose encoded byte size, congestion-window state or probe identity.

Before using a record to diagnose quality, perform one finite installed-runtime
feasibility check: keep the same source/codec/hardware and one viewer, explicitly
start once, wait for terminal status, Copy once, and verify all four marker kinds,
completion, loss status and achieved duration. Preserve partial results and do not
automatically repeat. Mocked callback and browser tests establish lifecycle behavior,
not support or overhead in the installed WebView2 runtime.

The fixed buffer configuration uses the official
[DevTools Tracing schema](https://github.com/ChromeDevTools/devtools-protocol/blob/master/json/browser_protocol.json).
