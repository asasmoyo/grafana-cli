package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

//go:embed skill/SKILL.md
var skillContent embed.FS

func installSkill(targetPath string) {
	if targetPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("finding home directory: %v", err)
		}
		targetPath = filepath.Join(home, ".claude", "skills", "grafana", "SKILL.md")
	}

	// Expand ~ to home directory
	if strings.HasPrefix(targetPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal("finding home directory: %v", err)
		}
		targetPath = filepath.Join(home, targetPath[2:])
	}

	skillPath := targetPath

	// Check if already installed
	if _, err := os.Stat(skillPath); err == nil {
		fmt.Printf("Skill already exists at %s\n", skillPath)
		fmt.Print("Overwrite? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Skipped.")
			return
		}
	}

	content, err := skillContent.ReadFile("skill/SKILL.md")
	if err != nil {
		fatal("reading embedded skill: %v", err)
	}

	skillDir := filepath.Dir(skillPath)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		fatal("creating directory %s: %v", skillDir, err)
	}

	if err := os.WriteFile(skillPath, content, 0644); err != nil {
		fatal("writing skill file: %v", err)
	}

	fmt.Printf("Installed skill to %s\n", skillPath)
	fmt.Println()
	fmt.Println("Make sure these environment variables are available to your agent:")
	fmt.Println("  GRAFANA_URL    — Grafana base URL (e.g. https://grafana.example.com)")
	fmt.Println("  GRAFANA_TOKEN  — Grafana Service Account token")
}

func usage() {
	fmt.Fprintf(os.Stderr, `grafana-cli — Query Grafana datasources (Prometheus, Loki, Tempo)

ENVIRONMENT:
  GRAFANA_URL    Grafana base URL (e.g. https://grafana.example.com)
  GRAFANA_TOKEN  Service Account token (Bearer token)

COMMANDS:
  install-skill [path]                     Install agent skill file (default: ~/.claude/skills/grafana/SKILL.md)
  datasources                              List all configured datasources

  prom query <datasource> <promql>         Instant PromQL query
    [--time <ts>]
    [--format tsv]                         Output as TSV (labels\tvalue)

  prom query-range <datasource> <promql>   Range PromQL query
    [--start <time>] [--end <time>]        Times: unix timestamp or relative (1h, 30m, 2d)
    [--step <step>]                        Step: 15s, 60s, 5m, etc. (default: 60s)
    [--format tsv]                         Output as TSV (timestamp\tvalue)

  prom labels <datasource>                 List all Prometheus label names
  prom label-values <datasource> <label>   List values for a label
  prom series <datasource> <match>         Find series matching selector

  loki query <datasource> <logql>          Query logs with LogQL
    [--start <time>] [--end <time>]        Times: unix ts, nanosecond ts, or relative (1h, 30m, 2d)
    [--limit <n>]                          Max log lines (default: 100)
    [--direction forward|backward]         Sort order: forward=oldest-first (default: backward)
    [--format tsv]                         Output as TSV (timestamp\tlog_line)

  loki count <datasource> <logql>          Count log volume per time bucket
    [--start <time>] [--end <time>]        (uses count_over_time metric query)
    [--step <step>]                        Bucket size (default: 1m)
    [--format tsv]                         Output as TSV (timestamp\tcount)

  loki labels <datasource>                 List all Loki label names
  loki label-values <datasource> <label>   List values for a label

  tempo trace <datasource> <traceID>       Get a trace by ID
  tempo search <datasource>                Search traces
    [--query <traceql>]                    TraceQL query (e.g. { .http.status_code = 500 })
    [--start <time>] [--end <time>]
    [--limit <n>]

EXAMPLES:
  grafana-cli datasources
  grafana-cli prom query prometheus 'up'
  grafana-cli prom query-range prometheus 'rate(http_requests_total[5m])' --start 2h --step 30s
  grafana-cli prom labels prometheus
  grafana-cli prom label-values prometheus job
  grafana-cli loki query loki '{app="api"} |= "error"' --start 1h --limit 50
  grafana-cli loki query loki '{app="api"} |= "error"' --start 1h --direction forward --limit 50
  grafana-cli loki count loki '{app="api"} |= "error"' --start 2h --step 1m
  grafana-cli loki labels loki
  grafana-cli tempo trace tempo abc123def456
  grafana-cli tempo search tempo --query '{ .http.status_code = 500 }' --start 1h

TIME FORMATS:
  Relative:     30m, 1h, 2d (lookback from now)
  Unix seconds: 1774452000 (auto-converted to nanos for Loki)
  Unix nanos:   1774452000000000000

DATASOURCE ARGUMENT:
  Can be a datasource name (or partial match), ID number, or type name.
  Use 'datasources' command to see what's available.
`)
	os.Exit(1)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func getFlag(args []string, flag string) (string, []string) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			val := args[i+1]
			rest := make([]string, 0, len(args)-2)
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+2:]...)
			return val, rest
		}
		if strings.HasPrefix(a, flag+"=") {
			val := strings.TrimPrefix(a, flag+"=")
			rest := make([]string, 0, len(args)-1)
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+1:]...)
			return val, rest
		}
	}
	return "", args
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage()
	}

	// Handle commands that don't need Grafana credentials
	if args[0] == "install-skill" {
		path := ""
		if len(args) > 1 {
			path = args[1]
		}
		installSkill(path)
		return
	}

	gc, err := NewGrafanaClient()
	if err != nil {
		fatal("%v", err)
	}

	cmd := args[0]
	args = args[1:]

	switch cmd {
	case "datasources", "ds":
		datasources, err := gc.ListDatasources()
		if err != nil {
			fatal("listing datasources: %v", err)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "ID\tUID\tNAME\tTYPE\tDEFAULT\n")
		for _, ds := range datasources {
			def := ""
			if ds.IsDefault {
				def = "*"
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", ds.ID, ds.UID, ds.Name, ds.Type, def)
		}
		w.Flush()

	case "prom", "prometheus":
		if len(args) == 0 {
			fatal("usage: grafana-cli prom <query|query-range|labels|label-values|series> <datasource> ...")
		}
		subcmd := args[0]
		args = args[1:]

		switch subcmd {
		case "query":
			if len(args) < 2 {
				fatal("usage: grafana-cli prom query <datasource> <promql> [--time <ts>] [--format tsv]")
			}
			dsName := args[0]
			query := args[1]
			args = args[2:]
			ts, args := getFlag(args, "--time")
			format, _ := getFlag(args, "--format")
			ds, err := gc.FindDatasource(dsName, "prometheus")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.PromQueryInstant(ds.ID, query, parseTimeFlag(ts), format)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		case "query-range", "range":
			if len(args) < 2 {
				fatal("usage: grafana-cli prom query-range <datasource> <promql> [--start <t>] [--end <t>] [--step <s>] [--format tsv]")
			}
			dsName := args[0]
			query := args[1]
			args = args[2:]
			start, args := getFlag(args, "--start")
			end, args := getFlag(args, "--end")
			step, args := getFlag(args, "--step")
			format, _ := getFlag(args, "--format")

			// Warn when the query range exceeds 6 hours — these often timeout.
			if dur := parseDurationSeconds(start); dur > 6*3600 {
				fmt.Fprintf(os.Stderr, "warning: --start %s is a %.0fh range — large Prometheus queries often timeout. Consider splitting into sequential queries.\n", start, float64(dur)/3600)
			}

			ds, err := gc.FindDatasource(dsName, "prometheus")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.PromQueryRange(ds.ID, query, parseTimeFlag(start), parseTimeFlag(end), step, format)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		case "labels":
			if len(args) < 1 {
				fatal("usage: grafana-cli prom labels <datasource>")
			}
			ds, err := gc.FindDatasource(args[0], "prometheus")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.PromLabels(ds.ID)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Println(result)

		case "label-values":
			if len(args) < 2 {
				fatal("usage: grafana-cli prom label-values <datasource> <label>")
			}
			ds, err := gc.FindDatasource(args[0], "prometheus")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.PromLabelValues(ds.ID, args[1])
			if err != nil {
				fatal("%v", err)
			}
			fmt.Println(result)

		case "series":
			if len(args) < 2 {
				fatal("usage: grafana-cli prom series <datasource> <match>")
			}
			ds, err := gc.FindDatasource(args[0], "prometheus")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.PromSeries(ds.ID, args[1])
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		default:
			fatal("unknown prom subcommand: %s (use query, query-range, labels, label-values, series)", subcmd)
		}

	case "loki":
		if len(args) == 0 {
			fatal("usage: grafana-cli loki <query|labels|label-values> <datasource> ...")
		}
		subcmd := args[0]
		args = args[1:]

		switch subcmd {
		case "query":
			if len(args) < 2 {
				fatal("usage: grafana-cli loki query <datasource> <logql> [--start <t>] [--end <t>] [--limit <n>] [--direction forward|backward] [--format tsv]")
			}
			dsName := args[0]
			query := args[1]
			args = args[2:]
			start, args := getFlag(args, "--start")
			end, args := getFlag(args, "--end")
			limitStr, args := getFlag(args, "--limit")
			direction, args := getFlag(args, "--direction")
			format, _ := getFlag(args, "--format")
			limit := defaultLimit
			if limitStr != "" {
				limit, _ = strconv.Atoi(limitStr)
			}
			ds, err := gc.FindDatasource(dsName, "loki")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.LokiQuery(ds.ID, query, parseTimeNano(start), parseTimeNano(end), limit, direction, format)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		case "count":
			if len(args) < 2 {
				fatal("usage: grafana-cli loki count <datasource> <logql> [--start <t>] [--end <t>] [--step <s>] [--format tsv]")
			}
			dsName := args[0]
			query := args[1]
			args = args[2:]
			start, args := getFlag(args, "--start")
			end, args := getFlag(args, "--end")
			step, args := getFlag(args, "--step")
			format, _ := getFlag(args, "--format")
			ds, err := gc.FindDatasource(dsName, "loki")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.LokiCount(ds.ID, query, parseTimeNano(start), parseTimeNano(end), step, format)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		case "labels":
			if len(args) < 1 {
				fatal("usage: grafana-cli loki labels <datasource>")
			}
			ds, err := gc.FindDatasource(args[0], "loki")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.LokiLabels(ds.ID)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Println(result)

		case "label-values":
			if len(args) < 2 {
				fatal("usage: grafana-cli loki label-values <datasource> <label>")
			}
			ds, err := gc.FindDatasource(args[0], "loki")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.LokiLabelValues(ds.ID, args[1])
			if err != nil {
				fatal("%v", err)
			}
			fmt.Println(result)

		default:
			fatal("unknown loki subcommand: %s (use query, count, labels, label-values)", subcmd)
		}

	case "tempo":
		if len(args) == 0 {
			fatal("usage: grafana-cli tempo <trace|search> <datasource> ...")
		}
		subcmd := args[0]
		args = args[1:]

		switch subcmd {
		case "trace":
			if len(args) < 2 {
				fatal("usage: grafana-cli tempo trace <datasource> <traceID>")
			}
			ds, err := gc.FindDatasource(args[0], "tempo")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.TempoTrace(ds.ID, args[1])
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		case "search":
			if len(args) < 1 {
				fatal("usage: grafana-cli tempo search <datasource> [--query <traceql>] [--start <t>] [--end <t>] [--limit <n>]")
			}
			dsName := args[0]
			args = args[1:]
			query, args := getFlag(args, "--query")
			start, args := getFlag(args, "--start")
			end, args := getFlag(args, "--end")
			limitStr, _ := getFlag(args, "--limit")
			limit := defaultLimit
			if limitStr != "" {
				limit, _ = strconv.Atoi(limitStr)
			}
			ds, err := gc.FindDatasource(dsName, "tempo")
			if err != nil {
				fatal("%v", err)
			}
			result, err := gc.TempoSearch(ds.ID, query, parseTimeFlag(start), parseTimeFlag(end), limit)
			if err != nil {
				fatal("%v", err)
			}
			fmt.Print(result)

		default:
			fatal("unknown tempo subcommand: %s (use trace, search)", subcmd)
		}

	default:
		fatal("unknown command: %s\nRun 'grafana-cli --help' for usage", cmd)
	}
}
