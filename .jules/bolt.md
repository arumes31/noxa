## 2024-10-21 - [Map Filtering Optimization]
**Learning:** In Go, filtering a map by a specific key can be optimized by using a direct map lookup instead of iterating over the entire map or copying the map values into a slice first.
**Action:** When filtering a map by key, check if the key is provided and use a direct map lookup `value, ok := map[key]` to avoid unnecessary allocations and O(N) operations.
