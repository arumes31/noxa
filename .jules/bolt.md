
## 2023-10-23 - [Optimizing custom emoji rendering loop]
**Learning:** Found a loop in chat-parsers.js iterating over potentially thousands of custom emojis for every chat text operation, performing costly HTML operations regardless of whether the emoji string even appeared in the text.
**Action:** When a function accepts a global/large array (like custom emojis) and applies a transformation across a string, pre-filter the array using a fast mechanism (like string.includes()) to find matches before applying the transformation logic.

## 2024-10-24 - Bulk INSERTS for Performance
**Learning:** Sequential `INSERT` statements in a loop lead to the N+1 query problem, increasing network latency and database overhead. Go `database/sql` can construct large batch `INSERT` statements by building parameterized query strings with `strings.Builder`.
**Action:** When updating database tables based on loops (like channel overrides), always aggregate the parameter arguments into slices and perform a single `tx.ExecContext` query using `VALUES (...), (...), ...`.
