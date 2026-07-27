package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// End-to-end tests for the command line itself, run against the fake Grafana
// rather than a live instance, so that argument handling, datasource resolution
// and route construction are all covered by `go test ./...` with no credentials.

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func cliBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "grafana-cli-test")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "grafana-cli")
		out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
		if err != nil {
			buildErr = err
			t.Logf("build output: %s", out)
		}
	})
	if buildErr != nil {
		t.Fatalf("building grafana-cli: %v", buildErr)
	}
	return binPath
}

type cliResult struct {
	stdout   string
	stderr   string
	combined string
	exitCode int
}

func runCLI(t *testing.T, url string, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GRAFANA_URL=" + url,
		"GRAFANA_TOKEN=test-token",
	}, env...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	res.combined = res.stdout + res.stderr
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running grafana-cli %v: %v", args, err)
	}
	return res
}

func TestCLISucceeds(t *testing.T) {
	f := newFakeGrafanaServer(t)

	tests := []struct {
		name    string
		args    []string
		outHas  []string
		outLack []string
	}{
		{
			name:   "datasources lists uid first",
			args:   []string{"datasources"},
			outHas: []string{"UID", "NAME", "TYPE", "DEFAULT", "ID", "PROMUID", "Google Cloud Monitoring"},
		},
		{
			// The selector the listing puts in the first column must round-trip.
			name:   "prom query by uid",
			args:   []string{"prom", "query", "PROMUID", "up"},
			outHas: []string{"METRIC", "VALUE", "up"},
		},
		{
			name:   "prom query by name",
			args:   []string{"prom", "query", "Prometheus", "up"},
			outHas: []string{"VALUE"},
		},
		{
			name:   "prom query by legacy numeric id still works on grafana 12",
			args:   []string{"prom", "query", "864", "up"},
			outHas: []string{"VALUE"},
		},
		{
			name:   "prom query-range with flags",
			args:   []string{"prom", "query-range", "Prometheus", "up", "--start", "1h", "--step", "5m"},
			outHas: []string{"up"},
		},
		{
			name:   "prom labels",
			args:   []string{"prom", "labels", "Prometheus"},
			outHas: []string{"__name__", "namespace"},
		},
		{
			name:   "loki query by exact name",
			args:   []string{"loki", "query", "Loki", `{app="api"}`, "--start", "30m", "--limit", "5"},
			outHas: []string{"boom", "namespace=default"},
		},
		{
			name:   "loki query tsv format",
			args:   []string{"loki", "query", "Loki", `{app="api"}`, "--format", "tsv"},
			outHas: []string{"\t"},
			// The tsv format is meant to be parsed, so no decoration.
			outLack: []string{"log lines returned"},
		},
		{
			name:   "loki count",
			args:   []string{"loki", "count", "Loki", `{app="api"}`, "--step", "5m"},
			outHas: []string{},
		},
		{
			name:   "tempo search",
			args:   []string{"tempo", "search", "Tempo", "--query", "{ duration > 1s }", "--start", "1h"},
			outHas: []string{},
		},
		{
			name:   "gcm projects",
			args:   []string{"gcm", "projects", "Google Cloud Monitoring"},
			outHas: []string{"my-project", "My Project"},
		},
		{
			name:   "gcm query",
			args:   []string{"gcm", "query", "GCMUID", "run_googleapis_com:request_count", "--project", "my-project", "--start", "1h"},
			outHas: []string{"us-central1-a", "0.42"},
		},
		{
			name:   "subcommand help exits successfully",
			args:   []string{"prom", "query-range", "--help"},
			outHas: []string{"usage: grafana-cli prom query-range"},
		},
		{
			name:   "compound and second-precision durations are accepted",
			args:   []string{"prom", "query-range", "Prometheus", "up", "--start", "1h30m", "--step", "90s"},
			outHas: []string{"up"},
		},
		{
			// RFC3339 is documented for every command, including the two whose
			// backends only understand epochs.
			name:   "absolute times on prometheus",
			args:   []string{"prom", "query-range", "Prometheus", "up", "--start", "2026-07-27T09:00:00Z", "--end", "2026-07-27T10:00:00Z"},
			outHas: []string{"up"},
		},
		{
			name:   "absolute times on loki",
			args:   []string{"loki", "query", "Loki", `{app="api"}`, "--start", "2026-07-27T09:00:00Z"},
			outHas: []string{"boom"},
		},
		{
			name:   "absolute times on tempo",
			args:   []string{"tempo", "search", "Tempo", "--start", "2026-07-27T09:00:00Z", "--end", "2026-07-27T10:00:00Z"},
			outHas: []string{},
		},
		{
			name:   "absolute times on gcm",
			args:   []string{"gcm", "query", "GCMUID", "up", "--project", "my-project", "--start", "2026-07-27T09:00:00Z"},
			outHas: []string{"us-central1-a"},
		},
		{
			name:   "absolute time with an offset",
			args:   []string{"prom", "query", "Prometheus", "up", "--time", "2026-07-27T12:00:00+02:00"},
			outHas: []string{"up"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runCLI(t, f.URL, nil, tt.args...)
			if res.exitCode != 0 {
				t.Fatalf("exit code = %d, want 0\n%s", res.exitCode, res.combined)
			}
			for _, want := range tt.outHas {
				if !strings.Contains(res.stdout, want) {
					t.Errorf("stdout does not contain %q:\n%s", want, res.stdout)
				}
			}
			for _, unwanted := range tt.outLack {
				if strings.Contains(res.stdout, unwanted) {
					t.Errorf("stdout unexpectedly contains %q:\n%s", unwanted, res.stdout)
				}
			}
		})
	}

	// Nothing above may have used the numeric-id routes Grafana 13 disabled.
	legacy := regexp.MustCompile(`/api/datasources/(proxy/)?[0-9]+(/|$)`)
	for _, p := range f.paths() {
		if legacy.MatchString(p) {
			t.Errorf("CLI used a legacy numeric-id datasource route: %s", p)
		}
	}
}

func TestCLIRejectsBadInput(t *testing.T) {
	f := newFakeGrafanaServer(t)

	tests := []struct {
		name   string
		args   []string
		errHas string
	}{
		{
			// Silently ignored before: the query ran with the default step.
			name:   "misspelled flag",
			args:   []string{"prom", "query-range", "Prometheus", "up", "--steps", "5m"},
			errHas: `unknown flag "--steps"`,
		},
		{
			name:   "unsupported format",
			args:   []string{"prom", "query", "Prometheus", "up", "--format", "json"},
			errHas: "expected one of: table, tsv",
		},
		{
			// Silently ignored before: only the first word was used as query.
			name:   "unquoted query",
			args:   []string{"prom", "query", "Prometheus", "sum(rate(x[5m]))", "by", "(job)"},
			errHas: "must be quoted",
		},
		{
			name:   "missing query",
			args:   []string{"prom", "query", "Prometheus"},
			errHas: "expected 2 argument(s), got 1",
		},
		{
			// Forwarded verbatim before, producing an opaque datasource error.
			name:   "unsupported duration spelling",
			args:   []string{"prom", "query-range", "Prometheus", "up", "--start", "1hour"},
			errHas: `invalid --start value "1hour"`,
		},
		{
			// Silently became the default limit before.
			name:   "non numeric limit",
			args:   []string{"loki", "query", "Loki", `{app="api"}`, "--limit", "abc"},
			errHas: "expected a whole number",
		},
		{
			name:   "zero limit",
			args:   []string{"loki", "query", "Loki", `{app="api"}`, "--limit", "0"},
			errHas: "greater than 0",
		},
		{
			name:   "invalid direction",
			args:   []string{"loki", "query", "Loki", `{app="api"}`, "--direction", "up"},
			errHas: "expected one of: forward, backward",
		},
		{
			name:   "flag value swallowed by the next flag",
			args:   []string{"tempo", "search", "Tempo", "--query", "--start", "1h"},
			errHas: "is another flag",
		},
		{
			// Used to proxy a LogQL query to Prometheus and fail confusingly.
			name:   "numeric id of the wrong type",
			args:   []string{"loki", "query", "864", `{app="api"}`},
			errHas: `requires a "loki" datasource`,
		},
		{
			name:   "name of the wrong type",
			args:   []string{"prom", "query", "Tempo", "up"},
			errHas: `requires a "prometheus" datasource`,
		},
		{
			// Used to pick whichever datasource sorted first.
			name:   "ambiguous datasource",
			args:   []string{"loki", "labels", "lok"},
			errHas: "is ambiguous",
		},
		{
			name:   "unknown datasource lists the alternatives",
			args:   []string{"prom", "labels", "nope"},
			errHas: "available prometheus datasources",
		},
		{
			name:   "unknown subcommand",
			args:   []string{"prom", "explode"},
			errHas: "unknown prom subcommand",
		},
		{
			name:   "gcm requires a project",
			args:   []string{"gcm", "query", "GCMUID", "up"},
			errHas: "--project is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runCLI(t, f.URL, nil, tt.args...)
			if res.exitCode == 0 {
				t.Fatalf("exit code = 0, want non-zero\n%s", res.combined)
			}
			if !strings.Contains(res.stderr, tt.errHas) {
				t.Errorf("stderr does not contain %q:\n%s", tt.errHas, res.stderr)
			}
			if res.stdout != "" {
				t.Errorf("errors must not write to stdout, got:\n%s", res.stdout)
			}
		})
	}
}

func TestCLIRejectsMissingCredentials(t *testing.T) {
	f := newFakeGrafanaServer(t)

	t.Run("missing token", func(t *testing.T) {
		res := runCLI(t, f.URL, []string{"GRAFANA_TOKEN="}, "datasources")
		if res.exitCode == 0 {
			t.Fatal("expected a non-zero exit code")
		}
		if !strings.Contains(res.stderr, "GRAFANA_TOKEN") {
			t.Errorf("stderr does not mention GRAFANA_TOKEN:\n%s", res.stderr)
		}
	})

	t.Run("missing url", func(t *testing.T) {
		res := runCLI(t, "", nil, "datasources")
		if res.exitCode == 0 {
			t.Fatal("expected a non-zero exit code")
		}
		if !strings.Contains(res.stderr, "GRAFANA_URL") {
			t.Errorf("stderr does not mention GRAFANA_URL:\n%s", res.stderr)
		}
	})

	t.Run("partial iap configuration", func(t *testing.T) {
		res := runCLI(t, f.URL, []string{"GRAFANA_IAP_CLIENT_ID=abc.apps.googleusercontent.com"}, "datasources")
		if res.exitCode == 0 {
			t.Fatal("expected a non-zero exit code")
		}
		if !strings.Contains(res.stderr, "GRAFANA_IAP_SA") {
			t.Errorf("stderr does not mention GRAFANA_IAP_SA:\n%s", res.stderr)
		}
	})
}

// The ambiguity note must go to stderr so that --format tsv output stays
// machine-readable.
func TestCLIDiagnosticsGoToStderr(t *testing.T) {
	f := newFakeGrafanaServer(t)

	res := runCLI(t, f.URL, nil, "prom", "query-range", "Prometheus", "up", "--start", "12h", "--format", "tsv")
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d\n%s", res.exitCode, res.combined)
	}
	if !strings.Contains(res.stderr, "warning:") {
		t.Errorf("expected a long-range warning on stderr, got:\n%s", res.stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		if strings.Contains(line, "warning") {
			t.Errorf("warning leaked into stdout: %q", line)
		}
	}
}

// Every time argument the CLI accepts must arrive at the datasource as a bare
// epoch in that backend's unit.
func TestCLISendsEpochsForEveryAcceptedTime(t *testing.T) {
	f := newFakeGrafanaServer(t)

	cases := []struct {
		name  string
		args  []string
		param string // query parameter to inspect; empty means the POST body
	}{
		{"prom", []string{"prom", "query-range", "Prometheus", "up", "--start", "2026-07-27T09:00:00Z"}, "start"},
		{"loki", []string{"loki", "query", "Loki", `{app="api"}`, "--start", "2026-07-27T09:00:00Z"}, "start"},
		{"tempo", []string{"tempo", "search", "Tempo", "--start", "2026-07-27T09:00:00Z"}, "start"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if res := runCLI(t, f.URL, nil, tt.args...); res.exitCode != 0 {
				t.Fatalf("exit %d\n%s", res.exitCode, res.combined)
			}
			got := f.last(t).Query.Get(tt.param)
			if !isUnixTimestamp(got) {
				t.Errorf("%s received %s=%q, which is not an epoch", tt.name, tt.param, got)
			}
		})
	}

	t.Run("gcm", func(t *testing.T) {
		args := []string{"gcm", "query", "GCMUID", "up", "--project", "p", "--start", "2026-07-27T09:00:00Z"}
		if res := runCLI(t, f.URL, nil, args...); res.exitCode != 0 {
			t.Fatalf("exit %d\n%s", res.exitCode, res.combined)
		}
		var payload struct{ From string }
		if err := json.Unmarshal([]byte(f.last(t).Body), &payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if !isUnixTimestamp(payload.From) {
			t.Errorf("gcm received from=%q, which is not an epoch", payload.From)
		}
	})
}
