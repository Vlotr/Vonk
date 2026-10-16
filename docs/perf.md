# Performance of page statistics

`go test -tags perf ./internal/jobs -run TestPerf -v -timeout 30m` (`internal/jobs/perf_test.go`) loads page views into a fresh SQLite database in a temp directory, which the test deletes when it ends (about 220 MB while it runs). It is not part of `make test`.

What it does:

1. 1 000 000 page views across 3 projects, 200 routes and 30 days, as visits of 1 to 8 page views (3 to 122 s apart, one tab each) by 40 000 visitors per project; routes follow a Zipf curve, 3 % of visits are bots, 80 % end with a `$pageleave`. Plus 20 000 error events with a route. Visits arrive in time order.
2. All of it goes through the real path from the queue on: `ingest.Queue` → one `ingest.Writer` → `store.SavePageEvents` (pairing page views with leaves and the next page view, marking dirty hours) and `store.SaveErrorEvents`, in batches of 500. One goroutine feeds the queue as fast as it takes items (it waits when the queue is full, as an SDK would after a 429).
3. The aggregation job's first run rebuilds every dirty project hour (3 × 721).
4. The dashboard's reads, each the median of five runs: the 30-day pages query of each project (`store.PageStats`: totals, 200 routes with visitors merged from the hourly sketches, views per hour) and the overview's page columns for the 3 projects (`store.PageviewTotals`, over 24 hours and over 30 days).

## Targets (controller ruling 20) and results

Measured on 2026-10-16 on this Mac: Apple M4 Pro (12 cores), 24 GB, macOS 27.0.1, Go 1.27.2, `modernc.org/sqlite` 1.60.1, an internal SSD that was 99 % full.

| What | Target | Result |
| --- | --- | --- |
| Writer | at least 5 000 events/s | 50 400 events/s: 1 197 629 events (1 000 000 page views, 177 629 page leaves, 20 000 errors) in 23.8 s |
| Aggregation of all data | under 60 s | 1.8 s for 2 163 project hours (286 746 hourly rows) |
| Pages query, 30 days | under 300 ms | 162 to 188 ms per project (about 95 000 hourly rows each; 322 000 views, 33 000 visitors) |
| Overview page columns | under 300 ms | 4 ms over 24 hours; 132 ms over 30 days |

Other numbers from the same run: the database was 216 MB; visitor sketches had a median of 9 bytes and a largest of 193 bytes (every route-hour had fewer than 4 096 visitors, so all were exact sets; a dense sketch is 16 KiB).

All targets are met. The pages query is the closest: its time grows with the number of route-hours in the range (here every one of 200 routes has views in nearly every hour), and it reads every row's sketch to merge visitors. A much slower machine (a small ARM board) could pass 300 ms on data this size; the fix then is a daily rollup of `hourly_stats` for ranges longer than a few days, not needed now.
