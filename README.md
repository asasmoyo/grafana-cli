# grafana-cli

A CLI for querying Grafana datasources (Prometheus, Loki, Tempo, Google Cloud Monitoring) through Grafana's API. Designed for use by AI coding agents (Claude, etc.) to consume monitoring data.

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

If your Grafana is behind Google Cloud IAP, also set:

```bash
export GRAFANA_IAP_CLIENT_ID="123456-abc.apps.googleusercontent.com"
export GRAFANA_IAP_SA="my-sa@my-project.iam.gserviceaccount.com"
```

This requires the `gcloud` CLI. The tool mints an IAP ID token via service account impersonation and sends it alongside the Grafana token using dual-header auth.

## Usage

```bash
# Discover available datasources (the UID column is the safest selector)
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

# Google Cloud Monitoring (via PromQL)
grafana-cli gcm projects "Google Cloud Monitoring"
grafana-cli gcm query "Google Cloud Monitoring" 'compute_googleapis_com:instance_cpu_utilization' --project my-project --start 1h
grafana-cli gcm query "Google Cloud Monitoring" 'avg by (zone) (compute_googleapis_com:instance_cpu_utilization)' --project my-project --start 1h --step 5m
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
- `grafana-cli gcm query <ds> '<promql>' --project <p> --start 1h` — GCM metrics via PromQL
- `grafana-cli gcm projects <ds>` — list GCP projects

### Investigation workflow
1. Check metrics: `grafana-cli prom query <ds> '<metric>'`
2. Check GCM metrics: `grafana-cli gcm query <ds> '<promql>' --project <p> --start 1h`
3. Estimate log volume: `grafana-cli loki count <ds> '{app="..."}' --start 2h --step 1m`
4. Find related logs: `grafana-cli loki query <ds> '{app="...",level="error"}' --direction forward`
5. Get trace details: `grafana-cli tempo trace <ds> <traceID>`
```

## Datasource Argument

Every command takes a datasource selector, resolved in this fixed order
(case-insensitive): **uid → numeric id → exact name → exact type → partial name
→ partial type**.

```bash
grafana-cli prom query dsuid-prometheus 'up'    # uid — always unambiguous
grafana-cli prom query Prometheus 'up'          # exact name
grafana-cli prom query prom 'up'                # partial name
```

The resolver never guesses:

- A selector matching several datasources is an error listing the candidates,
  unless exactly one of them is the Grafana default.
- A selector matching a datasource of the wrong type (`loki query` against a
  Prometheus datasource) is an error naming the type mismatch.
- Unknown selectors list the datasources that would have worked.

UIDs work on every supported Grafana version; the numeric id is the legacy
Grafana < 13 identity and is only kept for convenience.

## Time Formats

The `--start`, `--end` and `--time` flags accept:
- **Relative**: `90s`, `30m`, `1h`, `1h30m`, `2d`, `1w` (meaning "that long ago from now")
- **Unix timestamps**: `1711152000`, or nanoseconds `1711152000000000000`
- **RFC3339**: `2026-07-27T10:00:00Z`, including offsets (`2026-07-27T12:00:00+02:00`)
  and fractional seconds

Every form works with every command. Each is converted to the unit that
datasource expects — seconds for Prometheus and Tempo, nanoseconds for Loki,
milliseconds for Google Cloud Monitoring — so the same `--start` value produces
the same window everywhere, which is what makes metrics, logs and traces
comparable in one investigation.

Anything else (`1hour`, `90sec`, `yesterday`) is rejected up front instead of
being forwarded to the datasource as an opaque parse error.

## Strict Argument Handling

The CLI is mostly driven by agents, so input that would produce a *plausible but
wrong* answer is rejected:

| Input | Result |
|---|---|
| `--steps 5m` (typo) | error naming the accepted flags |
| `--limit abc`, `--limit 0` | error (previously silently became the default) |
| `--format json` | error listing `table, tsv` |
| `prom query ds sum(rate(x[5m])) by (job)` unquoted | error about quoting |
| `--query --start 1h` (missing value) | error, no accidental query |

Add `--help` to any subcommand to print its usage and exit 0.

Target a specific incident window with both `--start` and `--end`:
```bash
grafana-cli loki query loki '{app="api"} |= "error"' --start 1774452000 --end 1774453000
```

## Adapting to Your Environment

Nothing about a particular deployment is baked in — datasources, labels and
projects are all discovered at run time. The one presentation default that
assumes anything is label filtering, and it is overridable:

| Variable | Effect |
|---|---|
| `GRAFANA_HIDE_LABEL_PREFIXES` | Comma-separated label prefixes to hide from compact output. Replaces the defaults (which target Kubernetes/GKE noise). |
| `GRAFANA_HIDE_LABEL_PREFIXES=""` | Hide nothing; show every label. |

```bash
# EC2 tags are the noise here, Kubernetes labels are not present
export GRAFANA_HIDE_LABEL_PREFIXES="ec2_tag_,aws_"
```

Loki log lines are labelled with whichever stream labels the deployment uses:
`namespace`/`pod`/`container` are preferred when present, otherwise the stream's
own labels (`host`, `filename`, `unit`, ...) are shown.

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

## Google Cloud Monitoring

GCM metrics are queried using PromQL through Grafana's Cloud Monitoring datasource plugin. All requests go through Grafana's `/api/ds/query` endpoint — no direct GCP API access is needed.

### Discover projects
```bash
grafana-cli gcm projects "Google Cloud Monitoring"
```

### Query metrics
```bash
# CPU utilization across all instances
grafana-cli gcm query "Google Cloud Monitoring" \
  'compute_googleapis_com:instance_cpu_utilization' \
  --project my-project --start 1h

# Average by zone
grafana-cli gcm query "Google Cloud Monitoring" \
  'avg by (zone) (compute_googleapis_com:instance_cpu_utilization)' \
  --project my-project --start 1h --step 5m
```

### GCM metric naming in PromQL

GCM metrics use `service_com:metric_name` format:
- `compute_googleapis_com:instance_cpu_utilization` — GCE CPU
- `cloudsql_googleapis_com:database_cpu_utilization` — Cloud SQL CPU
- `run_googleapis_com:request_count` — Cloud Run requests
- `loadbalancing_googleapis_com:https_request_count` — Load balancer requests
