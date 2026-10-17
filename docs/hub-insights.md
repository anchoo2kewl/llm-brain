# Hub insights

Where the tokens, quota, time and reviews go: the fleet overview for today, and `/insights` for any range.

## Fleet overview

`/fleet` (or the home page, if you pick it in Settings). Long tables and logs sit behind expanders.

| Section | Shows |
| --- | --- |
| KPI tiles | The last 24 hours, with sparklines and the change against the previous day. |
| Top findings | The first few plain-English findings from Insights. |
| Issue pipeline | Queued, owned and PR-open issues from `status.json`, and the chat digest's asks of you. |
| Usage and capacity | A quota gauge per gateway account. |
| Routing | Which account is serving now. |
| Hosts | Activity per Mac. |
| Agents running now | Live workers. |
| LLMs and models | Calls by model and account. |
| Recent runs | The latest fleet runs. |
| Scheduler log | The tail of `fleet.log`. |

## Insights

`/insights?range=24h|7d|30d|all` (default `7d`). Charts are server-rendered SVG with no JavaScript.

| Section | Shows |
| --- | --- |
| What stands out | Plain-English findings. They come from fixed rules, not an LLM, and link to the chart they come from. |
| LLM usage | Token use by model and kind. |
| Gateway | Requests by account, latency p50 and p95, errors and 429s, personal vs work share, and when work was used. |
| Quota | Each quota window's headroom, burn rate and projected run-out. |
| Estimated cost | The API-equivalent value of your tokens. |
| Sessions | Sessions and prompts by host, a weekday by hour heatmap, top projects, tool calls and error rates, the longest and most expensive sessions. |
| Fleet | Runs by kind and outcome, review verdicts and blockers, busiest repos, cycle times. |
| Chat | Chat volume, mentions, busiest threads. |

Each session page and run page also has a strip with that conversation's tokens, estimated cost, tool calls and token use over time.

### Cost estimates

Costs use list prices in the `Prices` table in `hub/internal/insights/price.go` (US dollars per million input, output, cache-read and cache-write tokens). It is an estimate of what subscription tokens would have cost on the pay-as-you-go API, not a bill. Edit the table when prices change.

### How it stays fast

A background rollup reads messages in id order and condenses them into small tables. The first pass takes about 15 s per GB of messages; after that it keeps up incrementally. Reports are cached for a minute.

## JSON API

| Endpoint | Returns |
| --- | --- |
| `GET /api/insights?range=…` | The whole report. |
| `GET /api/insights/{section}?range=…` | One section: `kpis`, `callouts`, `llm`, `quota`, `cost`, `sessions`, `fleet` or `chat`. |
| `GET /api/insights/sessions/{id}` | One conversation and its subagents: tokens, cost, tool calls, token use over time. |

All are tailnet-only.
