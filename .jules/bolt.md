
## 2023-10-23 - [Optimizing custom emoji rendering loop]
**Learning:** Found a loop in chat-parsers.js iterating over potentially thousands of custom emojis for every chat text operation, performing costly HTML operations regardless of whether the emoji string even appeared in the text.
**Action:** When a function accepts a global/large array (like custom emojis) and applies a transformation across a string, pre-filter the array using a fast mechanism (like string.includes()) to find matches before applying the transformation logic.
