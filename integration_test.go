//go:build integration

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Contract tests for grafana-cli, run against a live Grafana.
//
// These are the only tests that can detect the datasource API contract moving
// underneath us — a route being removed (as the numeric-id datasource routes
// were in Grafana 13), a response shape changing, or a token losing a
// permission. Everything provable without a server lives in client_test.go,
// cli_test.go, flags_test.go, helpers_test.go and gcm_test.go and runs on every
// `go test ./...`.
//
// They are behind a build tag rather than a runtime skip on purpose: a suite
// that quietly skips reports success while proving nothing, which is how a
// broken client reached staging. `go test ./...` therefore never reports these
// as skipped, and asking for them without credentials is an error, not a no-op.
//
//	GRAFANA_URL=https://grafana.example.com \
//	GRAFANA_TOKEN=<service-account-token> \
//	go test -tags integration -v -run TestIntegration ./...
//
// Nothing here is specific to one deployment: datasources, labels, label values
// and projects are all discovered at run time, and a test skips with an explicit
// reason when the target Grafana has nothing to exercise it with. Where a
// deployment has several candidates, or where discovery cannot infer a
// meaningful query, point the suite at the right object:
//
//	GRAFANA_TEST_PROMETHEUS_DS   uid or name of the Prometheus datasource to use
//	GRAFANA_TEST_LOKI_DS         uid or name of the Loki datasource to use
//	GRAFANA_TEST_TEMPO_DS        uid or name of the Tempo datasource to use
//	GRAFANA_TEST_STACKDRIVER_DS  uid or name of the Cloud Monitoring datasource
//	GRAFANA_TEST_GCM_PROJECT     GCP project to query (default: first discovered)
//	GRAFANA_TEST_GCM_EXPR        GCM PromQL expression to query
//
// Run against every Grafana version you support before rolling out a change to
// datasource addressing or resolution.

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func requireGrafana(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAFANA_URL") == "" || os.Getenv("GRAFANA_TOKEN") == "" {
		t.Fatal("GRAFANA_URL and GRAFANA_TOKEN must be set: the integration build tag asks for tests that need a live Grafana")
	}
}

func mustClient(t *testing.T) *GrafanaClient {
	t.Helper()
	requireGrafana(t)
	gc, err := NewGrafanaClient()
	if err != nil {
		t.Fatalf("NewGrafanaClient: %v", err)
	}
	return gc
}

// datasourceOfType returns a datasource to exercise for the given type. It
// prefers an explicitly configured one, falls back to the first of that type,
// and skips the test when the deployment has none — a Grafana without Tempo is
// a valid deployment, not a failure.
func datasourceOfType(t *testing.T, gc *GrafanaClient, dsType string) *Datasource {
	t.Helper()

	envVar := "GRAFANA_TEST_" + strings.ToUpper(dsType) + "_DS"
	if selector := os.Getenv(envVar); selector != "" {
		ds, err := gc.FindDatasource(selector, dsType)
		if err != nil {
			t.Fatalf("%s=%q: %v", envVar, selector, err)
		}
		return ds
	}

	all, err := gc.ListDatasources()
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	for i := range all {
		if typeMatches(all[i], dsType) {
			return &all[i]
		}
	}
	t.Skipf("no %s datasource configured on %s — set %s to pin one",
		dsType, os.Getenv("GRAFANA_URL"), envVar)
	return nil
}

// nonEmptyLines splits a command result, failing when it produced nothing.
func nonEmptyLines(t *testing.T, what, result string) []string {
	t.Helper()
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		t.Fatalf("%s returned no output", what)
	}
	return strings.Split(trimmed, "\n")
}

// isSentinel reports whether a result is one of the documented "successful but
// empty" markers. Tests treat these as "nothing to assert on here".
func isSentinel(result string) bool {
	switch strings.TrimSpace(result) {
	case "(no results)", "(no data)", "(no log lines found)", "(no traces found)", "(no projects found)":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Client, discovery and selectors
// ---------------------------------------------------------------------------

func TestIntegrationListDatasources(t *testing.T) {
	gc := mustClient(t)

	datasources, err := gc.ListDatasources()
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	if len(datasources) == 0 {
		t.Fatal("expected at least one datasource")
	}

	for i, ds := range datasources {
		// The uid is the identity every query route uses, so it is the one
		// field this CLI cannot work without.
		if ds.UID == "" {
			t.Errorf("datasource %q has no uid", ds.Name)
		}
		if ds.Name == "" {
			t.Errorf("datasource %s has no name", ds.UID)
		}
		if ds.Type == "" {
			t.Errorf("datasource %s has no type", ds.UID)
		}
		if i > 0 && strings.ToLower(ds.Name) < strings.ToLower(datasources[i-1].Name) {
			t.Errorf("datasources not sorted by name: %q before %q", datasources[i-1].Name, ds.Name)
		}
	}

	types := map[string]int{}
	for _, ds := range datasources {
		types[ds.Type]++
	}
	t.Logf("%d datasources: %v", len(datasources), types)
}

// The selector contract, checked against whatever this deployment actually has.
func TestIntegrationDatasourceSelectors(t *testing.T) {
	gc := mustClient(t)

	all, err := gc.ListDatasources()
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	subject := all[0]

	t.Run("by_uid", func(t *testing.T) {
		ds, err := gc.FindDatasource(subject.UID, "")
		if err != nil {
			t.Fatalf("FindDatasource(%q): %v", subject.UID, err)
		}
		if ds.UID != subject.UID {
			t.Errorf("resolved to %q, want %q", ds.UID, subject.UID)
		}
	})

	t.Run("by_exact_name", func(t *testing.T) {
		ds, err := gc.FindDatasource(subject.Name, subject.Type)
		if err != nil {
			t.Fatalf("FindDatasource(%q): %v", subject.Name, err)
		}
		if ds.Type != subject.Type {
			t.Errorf("resolved to type %q, want %q", ds.Type, subject.Type)
		}
	})

	t.Run("by_numeric_id", func(t *testing.T) {
		if subject.ID == 0 {
			t.Skipf("%s reports no numeric id (expected on Grafana 13+)", os.Getenv("GRAFANA_URL"))
		}
		ds, err := gc.FindDatasource(strconv.Itoa(subject.ID), "")
		if err != nil {
			t.Fatalf("FindDatasource(%d): %v", subject.ID, err)
		}
		if ds.UID != subject.UID {
			t.Errorf("resolved to %q, want %q", ds.UID, subject.UID)
		}
	})

	t.Run("wrong_type_is_rejected", func(t *testing.T) {
		var other *Datasource
		for i := range all {
			if all[i].Type != subject.Type {
				other = &all[i]
				break
			}
		}
		if other == nil {
			t.Skip("deployment has only one datasource type")
		}
		if _, err := gc.FindDatasource(other.UID, subject.Type); err == nil {
			t.Errorf("resolving %s (%s) as a %q datasource should have failed", other.UID, other.Type, subject.Type)
		}
	})

	t.Run("unknown_selector", func(t *testing.T) {
		_, err := gc.FindDatasource("nonexistent-ds-xyz-123", "")
		if err == nil {
			t.Fatal("expected an error for an unknown datasource")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found', got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Prometheus
// ---------------------------------------------------------------------------

func TestIntegrationPrometheus(t *testing.T) {
	gc := mustClient(t)
	ds := datasourceOfType(t, gc, "prometheus")

	// vector(1) is answered by every Prometheus-compatible backend regardless of
	// what is being scraped, so it tests the route rather than the deployment.
	t.Run("instant_query", func(t *testing.T) {
		result, err := gc.PromQueryInstant(ds.UID, "vector(1)", "", "")
		if err != nil {
			t.Fatalf("PromQueryInstant: %v", err)
		}
		if isSentinel(result) {
			t.Fatalf("vector(1) returned %q", result)
		}
		if !strings.Contains(result, "VALUE") {
			t.Errorf("expected a VALUE column, got: %s", truncate(result, 200))
		}
	})

	t.Run("instant_query_at_a_timestamp", func(t *testing.T) {
		ts := fmt.Sprintf("%d", time.Now().Add(-30*time.Minute).Unix())
		result, err := gc.PromQueryInstant(ds.UID, "vector(1)", ts, "")
		if err != nil {
			t.Fatalf("PromQueryInstant at %s: %v", ts, err)
		}
		if isSentinel(result) {
			t.Errorf("vector(1) at -30m returned %q", result)
		}
	})

	t.Run("range_query", func(t *testing.T) {
		now := time.Now().UTC()
		result, err := gc.PromQueryRange(ds.UID, "vector(1)",
			fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix()),
			fmt.Sprintf("%d", now.Unix()), "5m", "")
		if err != nil {
			t.Fatalf("PromQueryRange: %v", err)
		}
		for _, want := range []string{"TIME", "VALUE", "samples"} {
			if !strings.Contains(result, want) {
				t.Errorf("expected %q in matrix output, got: %s", want, truncate(result, 200))
			}
		}
	})

	t.Run("range_query_defaults", func(t *testing.T) {
		result, err := gc.PromQueryRange(ds.UID, "vector(1)", "", "", "", "")
		if err != nil {
			t.Fatalf("PromQueryRange with defaults: %v", err)
		}
		if isSentinel(result) {
			t.Error("expected results for vector(1) over the default range")
		}
	})

	t.Run("empty_result_sentinel", func(t *testing.T) {
		result, err := gc.PromQueryInstant(ds.UID, "nonexistent_metric_xyz_12345", "", "")
		if err != nil {
			t.Fatalf("PromQueryInstant: %v", err)
		}
		if result != "(no results)" {
			t.Errorf("expected '(no results)', got: %s", truncate(result, 100))
		}
	})

	// Label discovery drives the remaining subtests, so they work against any
	// Prometheus regardless of what it scrapes.
	labels := nonEmptyLines(t, "PromLabels", mustPromLabels(t, gc, ds))

	t.Run("labels", func(t *testing.T) {
		// __name__ is part of the Prometheus data model, not of any deployment.
		for _, l := range labels {
			if l == "__name__" {
				t.Logf("%d labels", len(labels))
				return
			}
		}
		t.Errorf("expected __name__ among %d labels", len(labels))
	})

	t.Run("label_values", func(t *testing.T) {
		label := firstUsableLabel(labels)
		if label == "" {
			t.Skip("no non-reserved label to query values for")
		}
		result, err := gc.PromLabelValues(ds.UID, label)
		if err != nil {
			t.Fatalf("PromLabelValues(%q): %v", label, err)
		}
		values := nonEmptyLines(t, "PromLabelValues", result)
		t.Logf("label %q has %d values", label, len(values))
	})

	t.Run("series", func(t *testing.T) {
		label := firstUsableLabel(labels)
		if label == "" {
			t.Skip("no non-reserved label to build a selector from")
		}
		values, err := gc.PromLabelValues(ds.UID, label)
		if err != nil || strings.TrimSpace(values) == "" {
			t.Skipf("no values for label %q", label)
		}
		value := strings.Split(strings.TrimSpace(values), "\n")[0]
		selector := fmt.Sprintf("{%s=%q}", label, value)

		result, err := gc.PromSeries(ds.UID, selector)
		if err != nil {
			t.Fatalf("PromSeries(%s): %v", selector, err)
		}
		if strings.TrimSpace(result) == "" {
			t.Errorf("expected series for %s", selector)
		}
	})
}

func mustPromLabels(t *testing.T, gc *GrafanaClient, ds *Datasource) string {
	t.Helper()
	result, err := gc.PromLabels(ds.UID)
	if err != nil {
		t.Fatalf("PromLabels: %v", err)
	}
	return result
}

// firstUsableLabel picks a label suitable for building a selector, skipping
// Prometheus' reserved names.
func firstUsableLabel(labels []string) string {
	for _, l := range labels {
		if !strings.HasPrefix(l, "__") {
			return l
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Loki
// ---------------------------------------------------------------------------

func TestIntegrationLoki(t *testing.T) {
	gc := mustClient(t)
	ds := datasourceOfType(t, gc, "loki")

	labelsResult, err := gc.LokiLabels(ds.UID)
	if err != nil {
		t.Fatalf("LokiLabels: %v", err)
	}
	labels := nonEmptyLines(t, "LokiLabels", labelsResult)
	t.Logf("%d loki labels", len(labels))

	label := firstUsableLabel(labels)
	if label == "" {
		t.Skip("loki reports no usable stream label")
	}

	valuesResult, err := gc.LokiLabelValues(ds.UID, label)
	if err != nil {
		t.Fatalf("LokiLabelValues(%q): %v", label, err)
	}
	values := nonEmptyLines(t, "LokiLabelValues", valuesResult)
	selector := fmt.Sprintf("{%s=%q}", label, values[0])
	t.Logf("querying with discovered selector %s", selector)

	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-30*time.Minute).UnixNano())
	end := fmt.Sprintf("%d", now.UnixNano())

	t.Run("query", func(t *testing.T) {
		result, err := gc.LokiQuery(ds.UID, selector, start, end, 5, "", "")
		if err != nil {
			t.Fatalf("LokiQuery(%s): %v", selector, err)
		}
		if isSentinel(result) {
			t.Skipf("no log lines for %s in the last 30m", selector)
		}
		if !strings.Contains(result, "log lines returned") {
			t.Errorf("expected a line count footer, got: %s", truncate(result, 200))
		}
	})

	t.Run("limit_is_respected", func(t *testing.T) {
		result, err := gc.LokiQuery(ds.UID, selector, start, end, 2, "", "tsv")
		if err != nil {
			t.Fatalf("LokiQuery: %v", err)
		}
		if isSentinel(result) {
			t.Skipf("no log lines for %s in the last 30m", selector)
		}
		if n := len(nonEmptyLines(t, "LokiQuery", result)); n > 2 {
			t.Errorf("--limit 2 returned %d lines", n)
		}
	})

	t.Run("count", func(t *testing.T) {
		result, err := gc.LokiCount(ds.UID, selector, start, end, "5m", "")
		if err != nil {
			t.Fatalf("LokiCount: %v", err)
		}
		if isSentinel(result) {
			t.Skipf("no log volume for %s in the last 30m", selector)
		}
		if !strings.Contains(result, "total:") {
			t.Errorf("expected a total footer, got: %s", truncate(result, 200))
		}
	})
}

// ---------------------------------------------------------------------------
// Tempo
// ---------------------------------------------------------------------------

func TestIntegrationTempo(t *testing.T) {
	gc := mustClient(t)
	ds := datasourceOfType(t, gc, "tempo")

	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix())
	end := fmt.Sprintf("%d", now.Unix())

	result, err := gc.TempoSearch(ds.UID, "", start, end, 5)
	if err != nil {
		t.Fatalf("TempoSearch: %v", err)
	}
	if isSentinel(result) {
		t.Skip("no traces in the last hour")
	}
	if !strings.Contains(result, "TRACE_ID") {
		t.Fatalf("expected a TRACE_ID column, got: %s", truncate(result, 200))
	}

	t.Run("trace_by_id", func(t *testing.T) {
		traceID := firstTraceID(result)
		if traceID == "" {
			t.Skip("could not extract a trace id from the search results")
		}
		trace, err := gc.TempoTrace(ds.UID, traceID)
		if err != nil {
			t.Fatalf("TempoTrace(%s): %v", traceID, err)
		}
		if strings.TrimSpace(trace) == "" {
			t.Errorf("trace %s came back empty", traceID)
		}
	})
}

// firstTraceID reads a trace id out of the search table, skipping the header.
func firstTraceID(searchOutput string) string {
	for i, line := range strings.Split(strings.TrimSpace(searchOutput), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Google Cloud Monitoring
// ---------------------------------------------------------------------------

func TestIntegrationGCM(t *testing.T) {
	gc := mustClient(t)
	ds := datasourceOfType(t, gc, "stackdriver")

	result, err := gc.GCMProjects(ds.UID)
	if err != nil {
		t.Fatalf("GCMProjects: %v", err)
	}
	if isSentinel(result) {
		t.Skip("the Cloud Monitoring datasource reports no projects")
	}
	if !strings.Contains(result, "PROJECT_ID") {
		t.Fatalf("expected a PROJECT_ID column, got: %s", truncate(result, 200))
	}

	project := os.Getenv("GRAFANA_TEST_GCM_PROJECT")
	if project == "" {
		lines := nonEmptyLines(t, "GCMProjects", result)
		if len(lines) < 2 {
			t.Skip("no project rows to query with")
		}
		project = strings.Fields(lines[1])[0]
	}

	expr := os.Getenv("GRAFANA_TEST_GCM_EXPR")
	if expr == "" {
		t.Skipf("set GRAFANA_TEST_GCM_EXPR to a GCM PromQL expression to exercise queries against project %s", project)
	}

	t.Run("query", func(t *testing.T) {
		out, err := gc.GCMQuery(ds.UID, project, expr, "", "", "", "")
		if err != nil {
			t.Fatalf("GCMQuery(%s, %s): %v", project, expr, err)
		}
		t.Logf("result preview: %s", truncate(out, 200))
	})

	t.Run("invalid_query_is_reported", func(t *testing.T) {
		_, err := gc.GCMQuery(ds.UID, project, "this is not( valid promql", "", "", "", "")
		if err == nil {
			t.Fatal("expected an error for an invalid GCM expression")
		}
		if strings.Contains(err.Error(), "HTTP 400: {") {
			t.Errorf("raw JSON leaked into the error instead of a message: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// The binary itself
// ---------------------------------------------------------------------------

func TestIntegrationCLI(t *testing.T) {
	gc := mustClient(t)
	bin := cliBinary(t)

	env := append(os.Environ(),
		"GRAFANA_URL="+os.Getenv("GRAFANA_URL"),
		"GRAFANA_TOKEN="+os.Getenv("GRAFANA_TOKEN"),
	)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("datasources", func(t *testing.T) {
		out, err := run("datasources")
		if err != nil {
			t.Fatalf("datasources: %v\n%s", err, out)
		}
		for _, want := range []string{"UID", "NAME", "TYPE"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected a %s column:\n%s", want, out)
			}
		}
	})

	// The uid printed by `datasources` must be accepted by the query commands:
	// this is the round trip that broke on Grafana 13.
	t.Run("uid_round_trip", func(t *testing.T) {
		ds := datasourceOfType(t, gc, "prometheus")
		out, err := run("prom", "query", ds.UID, "vector(1)")
		if err != nil {
			t.Fatalf("prom query %s: %v\n%s", ds.UID, err, out)
		}
		if !strings.Contains(out, "VALUE") {
			t.Errorf("expected a VALUE column:\n%s", out)
		}
	})

	t.Run("tsv_output_is_bare", func(t *testing.T) {
		ds := datasourceOfType(t, gc, "prometheus")
		out, err := run("prom", "query", ds.UID, "vector(1)", "--format", "tsv")
		if err != nil {
			t.Fatalf("prom query --format tsv: %v\n%s", err, out)
		}
		if strings.Contains(out, "VALUE") {
			t.Errorf("tsv output should carry no table header:\n%s", out)
		}
	})
}

// TestIntegrationGrafanaVersion records which Grafana answered, so a failing
// run in the version matrix is self-describing.
func TestIntegrationGrafanaVersion(t *testing.T) {
	gc := mustClient(t)

	body, err := gc.get("/api/health")
	if err != nil {
		t.Skipf("/api/health is not readable with this token: %v", err)
	}
	t.Logf("%s health: %s", os.Getenv("GRAFANA_URL"), strings.TrimSpace(string(body)))
}
