# Headroom metrics reference

The exported metric names, pulled directly from the live proxy's `/metrics` endpoint
(`http://127.0.0.1:8787/metrics` in the VM). All series are prefixed `headroom_` and receive the
`host` / `vm` external labels (`host="darkness"`, `vm="fclaude"` by default) added by the in-VM
`prometheus --agent` on `remote_write` to Grafana Cloud.

> Metric availability/naming tracks the installed Headroom version. Regenerate this list after a major
> Headroom bump with: `curl -fsS http://127.0.0.1:8787/metrics | grep '^# HELP'` (run inside the VM,
> e.g. via `fclaude --maintenance`).

## Requests
| Metric | Meaning |
|---|---|
| `headroom_requests_total` | Total number of requests |
| `headroom_requests_cached_total` | Requests served from cache |
| `headroom_requests_failed_total` | Failed requests |
| `headroom_requests_rate_limited_total` | Rate-limited requests |
| `headroom_requests_by_model` | Requests broken out by model (label dimension) |
| `headroom_requests_by_provider` | Requests broken out by provider (label dimension) |
| `headroom_inbound_requests_total` | All inbound HTTP requests accepted by the proxy |
| `headroom_inbound_requests_completed_total` | Inbound requests completed or aborted |
| `headroom_inbound_requests_active` | Inbound requests currently active (gauge) |

## Tokens & savings (the compression payoff)
| Metric | Meaning |
|---|---|
| `headroom_tokens_saved_total` | Tokens saved by optimization |
| `headroom_tokens_input_total` | Total input tokens |
| `headroom_tokens_output_total` | Total output tokens |
| `headroom_uncached_input_tokens_total` | Input tokens not served from provider cache |
| `headroom_persistent_savings_tokens_saved_total` | **Durable lifetime** input tokens saved by compression |
| `headroom_persistent_savings_compression_savings_usd_total` | **Durable lifetime** compression savings (USD) |
| `headroom_persistent_savings_input_cost_usd_total` | Durable lifetime input spend (USD) |
| `headroom_persistent_savings_input_tokens_total` | Durable lifetime input tokens |
| `headroom_persistent_savings_requests_total` | Durable lifetime requests recorded |

The `persistent_savings_*` counters are cumulative lifetime totals (survive proxy restarts) — ideal for
headline "total tokens / $ saved" stat tiles.

## Provider cache
Compression can bust provider-side prompt caching; these track the trade-off.

| Metric | Meaning |
|---|---|
| `headroom_cache_bust_total` | Requests that lost provider cache efficiency due to compression |
| `headroom_cache_bust_tokens_lost_total` | Tokens that lost the provider cache discount |
| `headroom_cache_read_tokens_total` | Provider cache read tokens |
| `headroom_cache_write_tokens_total` | Provider cache write tokens |
| `headroom_cache_write_ttl_requests_total` | Provider cache write requests by observed TTL bucket |
| `headroom_cache_write_ttl_tokens_total` | Provider cache write tokens by observed TTL bucket |
| `headroom_provider_cache_requests_total` | Requests with provider cache observations |
| `headroom_provider_cache_hit_requests_total` | Requests with provider cache reads |
| `headroom_provider_cache_bust_total` | Provider-specific cache bust count |
| `headroom_provider_cache_bust_write_tokens_total` | Provider cache write tokens attributed to busts |

## Latency / timing
**⚠️ These are exposed as `min`/`max`/`sum`/`count` families — NOT Prometheus histograms with
`_bucket`.** Compute averages as `rate(<name>_sum[5m]) / rate(<name>_count[5m])`; `histogram_quantile`
does not apply.

| Family | Meaning |
|---|---|
| `headroom_latency_ms_{min,max,sum,count}` | Request latency (ms) |
| `headroom_ttfb_ms_{min,max,sum,count}` | Time-to-first-byte (ms) |
| `headroom_overhead_ms_{min,max,sum,count}` | Headroom's own added processing overhead (ms) |
| `headroom_stage_timing_ms_{sum,max,count}` | Per-stage handler timing (ms) |
| `headroom_transform_timing_ms_{sum,max,count}` | Transform timing (ms) |

## Misc
| Metric | Meaning |
|---|---|
| `headroom_active_ws_sessions` | Active Codex WebSocket sessions (gauge; typically 0 for Claude) |
| `headroom_active_relay_tasks` | Active Codex WS relay tasks (gauge) |
| `proxy_image_generation_call_log_redacted_total` | Base64 image payloads redacted from request logs |
| `wrap_rtk_invocations_total` | RTK invocations observed via the `wrap` CLI tail |

## Dashboarding notes
- **5-min scrape**: `rate()` over `[5m]`/`[10m]` on the `_total` counters works fine at this resolution.
- **Averages, not quantiles**: use `_sum / _count` for latency/overhead means (no `_bucket` series exist).
- **Headline savings tiles**: use the cumulative `headroom_persistent_savings_*` counters directly.
- **Labels**: every series carries `host` / `vm` (from `FCLAUDE_METRICS_HOST_LABEL` /
  `FCLAUDE_METRICS_VM_LABEL`), so multiple VMs/hosts pushing to the same Grafana Cloud stack stay
  distinguishable.
