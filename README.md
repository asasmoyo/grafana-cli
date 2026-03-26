# grafana-cli

A CLI for querying Grafana datasources (Prometheus, Loki, Tempo) through Grafana's datasource proxy API. Designed for use by AI coding agents (Claude, etc.) to consume monitoring data.

## Why This Approach

- **Single auth** — One Grafana Service Account token accesses all datasources (Prometheus, Loki, Tempo, etc.). No need to manage separate credentials.
- **Agent-friendly output** — Formats raw JSON into concise tables/text that won't blow up the context window.
- **Read-only by design** — Only queries data, never mutates.
- **Zero dependencies** — Pure Go stdlib. Single static binary.

## Setup

### 1. Create a Grafana Service Account Token

1. Go to Grafana → **Administration → Service Accounts**
2. Click **Add Service Account**, name it (e.g. `claude-agent`), role: **Viewer**
3. Click **Add Token**, copy the generated `glsa_...` token

### 2. Build & Install

```bash
go build -o grafana-cli .
ln -sf $(pwd)/grafana-cli /usr/local/bin/grafana-cli
```

### 3. Configure Environment

```bash
export GRAFANA_URL="https://grafana.example.com"
export GRAFANA_TOKEN="glsa_xxxxxxxxxxxxxxxxxxxx"
```

## Usage

```bash
# Discover available datasources
grafana-cli datasources

# Prometheus
grafana-cli prom query prometheus 'up'
grafana-cli prom query-range prometheus 'rate(http_requests_total[5m])' --start 2h --step 30s
grafana-cli prom labels prometheus
grafana-cli prom label-values prometheus job
grafana-cli prom series prometheus '{job="my-service"}'

# Loki
grafana-cli loki query loki '{app="api"} |= "error"' --start 1h --limit 50
grafana-cli loki query loki '{app="api"} |= "error"' --start 1h --direction forward --limit 50
grafana-cli loki count loki '{app="api"} |= "error"' --start 2h --step 1m
grafana-cli loki labels loki
grafana-cli loki label-values loki app

# Tempo
grafana-cli tempo search tempo --query '{ .http.status_code = 500 }' --start 1h
grafana-cli tempo trace tempo <traceID>
```

## Integrating with Claude / AI Agents

Add to your `CLAUDE.md` or agent system prompt:

```markdown
## Monitoring

You can query production monitoring data using `grafana-cli`. Environment is pre-configured.

### Quick reference
- `grafana-cli datasources` — list available datasources
- `grafana-cli prom query <ds> '<promql>'` — instant Prometheus query  
- `grafana-cli prom query-range <ds> '<promql>' --start 1h --step 30s` — range query
- `grafana-cli loki query <ds> '{app="api"} |= "error"' --start 1h --limit 50` — search logs
- `grafana-cli tempo search <ds> --query '{ .http.status_code >= 500 }' --start 1h` — find traces
- `grafana-cli tempo trace <ds> <traceID>` — get full trace

### Investigation workflow
1. Check metrics: `grafana-cli prom query <ds> '<metric>'`
2. Estimate log volume: `grafana-cli loki count <ds> '{app="..."}' --start 2h --step 1m`
3. Find related logs: `grafana-cli loki query <ds> '{app="...",level="error"}' --direction forward`
4. Get trace details: `grafana-cli tempo trace <ds> <traceID>`
```

## Time Formats

The `--start` and `--end` flags accept:
- **Relative**: `30m`, `1h`, `2d` (meaning "that long ago from now")
- **Unix timestamps**: `1711152000` (auto-converted to nanos for Loki)
- **Nanosecond timestamps** (Loki): `1711152000000000000`

Target a specific incident window with both `--start` and `--end`:
```bash
grafana-cli loki query loki '{app="api"} |= "error"' --start 1774452000 --end 1774453000
```

## Output Formats

Default output is human-readable tables. Use `--format tsv` for pipe-friendly output:
```bash
# Grep/awk/sort-friendly log output (timestamp\tlog_line)
grafana-cli loki query loki '{app="api"}' --start 1h --format tsv | grep "timeout" | wc -l

# Prometheus TSV (timestamp\tvalue for range, labels\tvalue for instant)
grafana-cli prom query-range prometheus 'rate(http_errors[5m])' --start 1h --format tsv | sort -t$'\t' -k2 -rn | head
```

## Volume Estimation

Before fetching raw logs, check the volume with `loki count`:
```bash
# How many error logs per minute in the last 2 hours?
grafana-cli loki count loki '{app="api"} |= "error"' --start 2h --step 1m
```
This uses `count_over_time` and shows lines-per-bucket, so you know whether
`--limit 50` will cover 1 second or 1 hour of data.
