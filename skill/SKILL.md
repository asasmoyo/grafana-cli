---
name: grafana
description: Query Grafana observability stack (Prometheus metrics, Loki logs, Tempo traces) via grafana-cli. Use when investigating production issues, checking service health, querying metrics/logs/traces, debugging errors, or answering questions about infrastructure and application behavior.
---

# Grafana Observability Skill

Query Prometheus metrics, Loki logs, and Tempo traces from your Grafana instance using `grafana-cli`.

## Prerequisites

- `grafana-cli` must be installed and in your `$PATH` (`go install github.com/asasmoyo/grafana-cli@latest`)
- Environment variables must be set:
  - `GRAFANA_URL` — Grafana base URL (e.g. `https://grafana.example.com` or `http://localhost:3000`)
  - `GRAFANA_TOKEN` — Grafana Service Account token
  - `GRAFANA_IAP_CLIENT_ID` — Google Cloud IAP OAuth Client ID (optional, only if Grafana is behind IAP)
  - `GRAFANA_IAP_SA` — Service account to impersonate for IAP auth (optional, requires `gcloud` CLI)

## Discover Your Datasources

Before querying, list available datasources to find the correct names:

```bash
grafana-cli datasources
```

Use the datasource **name** (or a partial match) in all commands below. Common names: `Prometheus`, `Loki`, `Tempo`.

## Commands

### Prometheus

**Instant query:**
```bash
grafana-cli prom query <datasource> '<promql>'
```

**Range query:**
```bash
grafana-cli prom query-range <datasource> '<promql>' --start <time> --step <step>
```

**List labels:**
```bash
grafana-cli prom labels <datasource>
```

**Label values:**
```bash
grafana-cli prom label-values <datasource> <label>
```

**Find series:**
```bash
grafana-cli prom series <datasource> '<selector>'
```

### Loki

**Query logs:**
```bash
grafana-cli loki query <datasource> '<logql>' --start <time> --limit <n>
```

**Count log volume:**
```bash
grafana-cli loki count <datasource> '<logql>' --start <time> --step <step>
```

**List labels:**
```bash
grafana-cli loki labels <datasource>
```

**Label values:**
```bash
grafana-cli loki label-values <datasource> <label>
```

### Tempo

**Search traces:**
```bash
grafana-cli tempo search <datasource> --start <time> --limit <n>
```

**Search with TraceQL:**
```bash
grafana-cli tempo search <datasource> --query '<traceql>' --start <time> --limit <n>
```

**Get trace by ID:**
```bash
grafana-cli tempo trace <datasource> <traceID>
```

## Time Parameters

- Relative: `30m`, `1h`, `2h`, `6h`, `1d`, `7d`
- Absolute: Unix timestamp (e.g. `1774335000`)
- Step (Prometheus range queries): `15s`, `60s`, `5m`, `15m`

## Investigation Workflow

When investigating a production issue, follow this order:

1. **Discover datasources** — `grafana-cli datasources`
2. **Check service health** — `grafana-cli prom query <ds> 'up{namespace="<ns>"}'`
3. **Check error rates** — `grafana-cli prom query <ds> 'sum(rate(http_requests_total{code=~"5.."}[5m])) by (service)'`
4. **Look at recent errors in logs** — `grafana-cli loki query <ds> '{namespace="<ns>"} |= "error"' --start 1h --limit 20`
5. **Check resource usage** — `grafana-cli prom query-range <ds> 'sum(rate(container_cpu_usage_seconds_total{namespace="<ns>"}[5m])) by (pod)' --start 1h --step 5m`
6. **Find slow traces** — `grafana-cli tempo search <ds> --query '{ duration > 1s }' --start 1h --limit 10`
7. **Drill into a trace** — `grafana-cli tempo trace <ds> <traceID>`

## IAP-Protected Grafana

If your Grafana instance is behind Google Cloud Identity-Aware Proxy, set both IAP variables:

```bash
export GRAFANA_IAP_CLIENT_ID="123456-abc.apps.googleusercontent.com"
export GRAFANA_IAP_SA="my-sa@my-project.iam.gserviceaccount.com"
```

This uses `gcloud auth print-identity-token` to mint an ID token. The IAP token is sent via `Proxy-Authorization` header while the Grafana token uses the standard `Authorization` header.

## Tips

- The datasource argument accepts partial names: `Prom`, `Loki`, `Tempo` all work.
- Loki max query range is 30 days. Use `--start 30m` or `--start 1h` for recent logs.
- Output is compact: noisy Kubernetes labels are hidden by default.
- For Prometheus, prefer `sum(...) by (label)` to reduce output volume.
- For Loki, always add a filter like `|= "error"` or `|= "timeout"` to narrow results.
- All commands support `--format tsv` for machine-readable output.
