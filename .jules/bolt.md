
## 2023-10-23 - [Optimizing custom emoji rendering loop]
**Learning:** Found a loop in chat-parsers.js iterating over potentially thousands of custom emojis for every chat text operation, performing costly HTML operations regardless of whether the emoji string even appeared in the text.
**Action:** When a function accepts a global/large array (like custom emojis) and applies a transformation across a string, pre-filter the array using a fast mechanism (like string.includes()) to find matches before applying the transformation logic.

## 2023-10-24 - Bulk Insert with UNNEST Array
**Learning:** In Go using `github.com/lib/pq`, executing N separate `INSERT` statements in a loop for database operations significantly impacts performance due to N round-trips over the network and DB planning overhead. Using PostgreSQL's `UNNEST` with `pq.Array()` converts the N inserts into a single atomic bulk insert operation.
**Action:** When working on DB-bound logic with arrays/lists, identify loops making discrete queries. Batch them together using native database array functions or bulk inserts like `UNNEST` to save network and compute resources.
