## 2026-10-24 - [N+1 Query in auth_role_grants insert]
**Learning:** [When dealing with bulk inserts based on lists in Go structs, `pq.Array` with `UNNEST(::type)` gives an efficient way to execute everything as a single database statement rather than looping.]
**Action:** [Check loops executing identical INSERT statements over and over inside transactions and replace with `UNNEST(pq.Array(...))` to prevent N+1 queries.]
