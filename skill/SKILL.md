---
name: grafana
description: Query Grafana observability stack (Prometheus metrics, Loki logs, Tempo traces, Google Cloud Monitoring) via grafana-cli. Use when investigating production issues, checking service health, querying metrics/logs/traces, debugging errors, or answering questions about infrastructure and application behavior.
---

# Grafana Observability Skill

Query Prometheus metrics, Loki logs, Tempo traces, and Google Cloud Monitoring metrics from your Grafana instance using `grafana-cli`.

## Prerequisites

- `grafana-cli` must be installed and in your `$PATH` (`go install github.com/asasmoyo/grafana-cli@latest`)
- Environment variables must be set:
  - `GRAFANA_URL` — Grafana base URL (e.g. `https://grafana.example.com` or `http://localhost:3000`)
  - `GRAFANA_TOKEN` — Grafana Service Account token
  - `GRAFANA_IAP_CLIENT_ID` — Google Cloud IAP OAuth Client ID (optional, only if Grafana is behind IAP)
  - `GRAFANA_IAP_SA` — Service account to impersonate for IAP auth (optional, requires `gcloud` CLI)
  - `GRAFANA_HIDE_LABEL_PREFIXES` — label prefixes to hide from compact output (optional; defaults to Kubernetes/GKE noise, set empty to show every label)

## Start Here: Discover Datasources

```bash
grafana-cli datasources
```

```
UID               NAME                     TYPE         DEFAULT  ID
dsuid-prometheus  Prometheus               prometheus   *        864
dsuid-loki        Loki                     loki                  12
```

**Use the UID column** as the datasource argument in every other command. Names
and partial names also work, but a UID can never be ambiguous and never breaks
when a datasource is renamed:

```bash
grafana-cli prom query dsuid-prometheus 'up'    # preferred
grafana-cli prom query Prometheus 'up'          # fine when the name is unique
```

Run `datasources` once at the start of an investigation and reuse the UIDs.

## Commands

### Prometheus

```bash
grafana-cli prom query <ds> '<promql>' [--time <t>] [--format tsv]
grafana-cli prom query-range <ds> '<promql>' [--start <t>] [--end <t>] [--step <s>] [--format tsv]
grafana-cli prom labels <ds>
grafana-cli prom label-values <ds> <label>
grafana-cli prom series <ds> '<selector>'
```

### Loki

```bash
grafana-cli loki query <ds> '<logql>' [--start <t>] [--end <t>] [--limit <n>] [--direction forward|backward] [--format tsv]
grafana-cli loki count <ds> '<logql>' [--start <t>] [--end <t>] [--step <s>] [--format tsv]
grafana-cli loki labels <ds>
grafana-cli loki label-values <ds> <label>
```

### Tempo

```bash
grafana-cli tempo search <ds> [--query '<traceql>'] [--start <t>] [--end <t>] [--limit <n>]
grafana-cli tempo trace <ds> <traceID>
```

### Google Cloud Monitoring

```bash
grafana-cli gcm projects <ds>
grafana-cli gcm query <ds> '<promql>' --project <project> [--start <t>] [--end <t>] [--step <s>] [--format tsv]
```

GCM metrics use `service_com:metric_name` format in PromQL:
- `compute_googleapis_com:instance_cpu_utilization` — GCE CPU
- `cloudsql_googleapis_com:database_cpu_utilization` — Cloud SQL CPU
- `run_googleapis_com:request_count` — Cloud Run requests

Any subcommand accepts `--help` and prints its exact usage.

## Argument Rules

The CLI rejects anything ambiguous instead of guessing, so a rejected command
means "fix the command", not "the data is missing".

- **Times** (`--start`, `--end`, `--time`): `90s`, `30m`, `1h`, `1h30m`, `2d`,
  `1w`, a unix timestamp, or RFC3339 (`2026-07-27T10:00:00Z`, offsets allowed).
  `1hour`/`90sec`/`yesterday` are errors. Every form works with every command,
  so use one absolute `--start`/`--end` pair across metrics, logs and traces to
  keep an investigation on a single time window.
- **Steps** (`--step`): `15s`, `60s`, `5m`, `1h`.
- **Quote every query.** An unquoted PromQL/LogQL query is split by the shell
  and rejected: always `'sum(rate(x[5m])) by (job)'`.
- **Flags are strict.** A typo like `--steps` is an error listing valid flags;
  `--limit abc` and `--format json` are errors. Only `--format tsv` (or the
  default `table`) exists.

## Reading the Output

- `(no results)`, `(no log lines found)`, `(no data)`, `(no traces found)` mean
  the query was **successful and empty** — the exit code is 0. Do not retry the
  same query; widen the time window or loosen the selector instead.
- Any failure exits non-zero and prints `error: ...` on **stderr**. Warnings and
  notes also go to stderr, so `--format tsv` output on stdout stays parseable.
- Output is truncated to protect the context window: 200 instant results, 50
  series, 50 samples per series, 100 log lines by default, 500 characters per
  log line. Truncation is always announced (`... (N more series truncated)`);
  if you see it, aggregate the query rather than raising the limit.
- Noisy labels are hidden and summarised as `(+N labels)`. The default list
  targets Kubernetes/GKE; if a label you need is missing from the output, re-run
  with `GRAFANA_HIDE_LABEL_PREFIXES=` set to empty to see everything.

## Failure Playbook

| Message | Meaning and fix |
|---|---|
| `datasource "x" is ambiguous` | Several datasources matched. Re-run with the UID printed in the error. |
| `is of type "loki", but this command requires a "prometheus" datasource` | Wrong subcommand for that datasource — the error lists the ones that fit. |
| `datasource "x" not found` | The error lists valid datasources; pick one. |
| `Grafana auth failed (HTTP 401/403)` | `GRAFANA_TOKEN` is wrong or lacks permission. Not retryable. |
| `IAP authentication failed` | `GRAFANA_IAP_CLIENT_ID`/`GRAFANA_IAP_SA` wrong, or `gcloud` not authenticated. |
| `HTTP 404: {"message":"Not found"}` | The datasource proxy route is unavailable — report it, do not retry. |
| `request failed: ... context deadline exceeded` | The query took over 30s. Narrow the range, raise `--step`, or aggregate. |
| `warning: --start 12h is a 12h range` | Prometheus will likely time out; split into sequential shorter queries. |

## Query Efficiency

Every query costs latency and context. In order of preference:

1. **Aggregate in the query, not after it**: `sum by (service) (rate(http_requests_total{code=~"5.."}[5m]))`
   returns a handful of rows; the raw metric returns thousands.
2. **Size before you fetch**: `loki count` shows lines per bucket, so you know
   whether `--limit 100` covers 10 seconds or 10 hours.
3. **Always filter logs**: `{app="api"} |= "error"` — never `{app="api"}` alone.
4. **Match `--step` to the window**: ~50–200 points is plenty. 1h → `30s`,
   6h → `5m`, 1d → `15m`. A 1d window at `15s` is 5760 points per series and
   will time out.
5. **Pick the format by destination.** If you are going to *read* the output,
   keep the default table — it is the most compact form. If you are going to
   *pipe* it into `sort`/`awk`/`wc`, use `--format tsv`.
   Caveat for logs: log lines routinely contain tab characters, and `--format
   tsv` does not escape them, so a `cut -f2` on Loki output can silently pick up
   the wrong field. Prefer the default format when reading log lines, and
   restrict TSV piping to metric output, whose values never contain tabs.

## Investigation Workflow

1. **Discover** — `grafana-cli datasources`, note the UIDs.
2. **Establish the window** — find when the symptom started; use the same
   `--start`/`--end` for every subsequent query so metrics, logs and traces line up.
3. **Health** — `prom query <ds> 'up{namespace="<ns>"} == 0'`
4. **Error rate** — `prom query <ds> 'sum by (service) (rate(http_requests_total{code=~"5.."}[5m]))'`
5. **Saturation** — CPU/memory/latency:
   `prom query-range <ds> 'sum by (pod) (rate(container_cpu_usage_seconds_total{namespace="<ns>"}[5m]))' --start 1h --step 1m`
6. **Log volume, then logs** — `loki count <ds> '{namespace="<ns>"} |= "error"' --start 2h --step 5m`,
   then `loki query <ds> '{namespace="<ns>"} |= "error"' --start 1h --limit 20`
7. **Traces** — `tempo search <ds> --query '{ duration > 1s }' --start 1h --limit 10`,
   then `tempo trace <ds> <traceID>` for the slowest one.
8. **Cloud resources** — `gcm query <ds> 'cloudsql_googleapis_com:database_cpu_utilization' --project <p> --start 1h`

Correlate by shared labels: `namespace`, `pod`, `service`/`service_name`, and
`trace_id` in log lines links Loki to Tempo.

## Discovering What Exists

Do not guess metric or label names:

```bash
grafana-cli prom label-values <ds> __name__        # every metric name
grafana-cli prom series <ds> '{job="my-service"}'  # label sets for a selector
grafana-cli prom labels <ds>                       # every label name
grafana-cli loki labels <ds>                       # every Loki label
grafana-cli loki label-values <ds> namespace       # values for one label
```

## IAP-Protected Grafana

If your Grafana instance is behind Google Cloud Identity-Aware Proxy, set both IAP variables:

```bash
export GRAFANA_IAP_CLIENT_ID="123456-abc.apps.googleusercontent.com"
export GRAFANA_IAP_SA="my-sa@my-project.iam.gserviceaccount.com"
```

This uses `gcloud auth print-identity-token` to mint an ID token. The IAP token is sent via `Proxy-Authorization` header while the Grafana token uses the standard `Authorization` header.
