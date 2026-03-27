package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Integration tests for grafana-cli.
//
// These tests require a live Grafana instance with Prometheus, Loki, and Tempo
// datasources configured. Set the following environment variables:
//
//   GRAFANA_URL=http://localhost:8080
//   GRAFANA_TOKEN=<service-account-token>
//
// Run with: go test -v -tags integration -run TestIntegration
//
// The tests are designed to be run against a Grafana instance (e.g. via
// kubectl port-forward) with Prometheus, Loki, and Tempo datasources
// configured.

func skipIfNoGrafana(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAFANA_URL") == "" || os.Getenv("GRAFANA_TOKEN") == "" {
		t.Skip("GRAFANA_URL and GRAFANA_TOKEN must be set for integration tests")
	}
}

func mustClient(t *testing.T) *GrafanaClient {
	t.Helper()
	skipIfNoGrafana(t)
	gc, err := NewGrafanaClient()
	if err != nil {
		t.Fatalf("NewGrafanaClient: %v", err)
	}
	return gc
}

func mustFindDatasource(t *testing.T, gc *GrafanaClient, name, dsType string) *Datasource {
	t.Helper()
	ds, err := gc.FindDatasource(name, dsType)
	if err != nil {
		t.Fatalf("FindDatasource(%q, %q): %v", name, dsType, err)
	}
	return ds
}

// ---------------------------------------------------------------------------
// Client & Datasource discovery
// ---------------------------------------------------------------------------

func TestIntegrationNewGrafanaClient(t *testing.T) {
	skipIfNoGrafana(t)

	gc, err := NewGrafanaClient()
	if err != nil {
		t.Fatalf("NewGrafanaClient failed: %v", err)
	}
	if gc.baseURL == "" {
		t.Error("baseURL is empty")
	}
	if gc.token == "" {
		t.Error("token is empty")
	}
	if gc.client == nil {
		t.Error("http client is nil")
	}
}

func TestIntegrationListDatasources(t *testing.T) {
	gc := mustClient(t)

	datasources, err := gc.ListDatasources()
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	if len(datasources) == 0 {
		t.Fatal("expected at least one datasource")
	}

	// Verify sorted by ID
	for i := 1; i < len(datasources); i++ {
		if datasources[i].ID < datasources[i-1].ID {
			t.Errorf("datasources not sorted by ID: [%d].ID=%d < [%d].ID=%d",
				i, datasources[i].ID, i-1, datasources[i-1].ID)
		}
	}

	// Verify required fields are populated
	for _, ds := range datasources {
		if ds.ID == 0 {
			t.Error("datasource has zero ID")
		}
		if ds.UID == "" {
			t.Errorf("datasource %d has empty UID", ds.ID)
		}
		if ds.Name == "" {
			t.Errorf("datasource %d has empty Name", ds.ID)
		}
		if ds.Type == "" {
			t.Errorf("datasource %d has empty Type", ds.ID)
		}
	}

	t.Logf("found %d datasources", len(datasources))
}

func TestIntegrationFindDatasource(t *testing.T) {
	gc := mustClient(t)

	t.Run("by_name", func(t *testing.T) {
		ds, err := gc.FindDatasource("Prometheus", "prometheus")
		if err != nil {
			t.Fatalf("FindDatasource by name: %v", err)
		}
		if !strings.Contains(strings.ToLower(ds.Type), "prometheus") {
			t.Errorf("expected prometheus type, got %q", ds.Type)
		}
	})

	t.Run("by_id", func(t *testing.T) {
		// First find it by name to get the ID
		ds, err := gc.FindDatasource("Prometheus", "prometheus")
		if err != nil {
			t.Fatalf("FindDatasource: %v", err)
		}
		// Then find by ID
		ds2, err := gc.FindDatasource(strconv.Itoa(ds.ID), "")
		if err != nil {
			t.Fatalf("FindDatasource by ID: %v", err)
		}
		if ds2.ID != ds.ID {
			t.Errorf("ID mismatch: got %d, want %d", ds2.ID, ds.ID)
		}
	})

	t.Run("by_type", func(t *testing.T) {
		ds, err := gc.FindDatasource("loki", "")
		if err != nil {
			t.Fatalf("FindDatasource by type: %v", err)
		}
		if !strings.Contains(strings.ToLower(ds.Type), "loki") {
			t.Errorf("expected loki type, got %q", ds.Type)
		}
	})

	t.Run("partial_name", func(t *testing.T) {
		ds, err := gc.FindDatasource("prom", "prometheus")
		if err != nil {
			t.Fatalf("FindDatasource partial: %v", err)
		}
		if !strings.Contains(strings.ToLower(ds.Type), "prometheus") {
			t.Errorf("expected prometheus type, got %q", ds.Type)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		_, err := gc.FindDatasource("nonexistent-ds-xyz-123", "")
		if err == nil {
			t.Error("expected error for nonexistent datasource")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected 'not found' in error, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Prometheus
// ---------------------------------------------------------------------------

func TestIntegrationPromQueryInstant(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Prometheus", "prometheus")

	t.Run("simple_query", func(t *testing.T) {
		result, err := gc.PromQueryInstant(ds.ID, "count(up)", "", "")
		if err != nil {
			t.Fatalf("PromQueryInstant: %v", err)
		}
		if result == "(no results)" {
			t.Fatal("expected results for count(up)")
		}
		if !strings.Contains(result, "VALUE") {
			t.Error("expected VALUE header in tabwriter output")
		}
		// count(up) should return a number > 0
		lines := strings.Split(strings.TrimSpace(result), "\n")
		if len(lines) < 2 {
			t.Fatalf("expected at least header + 1 row, got %d lines", len(lines))
		}
		t.Logf("count(up) result: %s", strings.TrimSpace(lines[1]))
	})

	t.Run("with_timestamp", func(t *testing.T) {
		ts := fmt.Sprintf("%d", time.Now().Add(-30*time.Minute).Unix())
		result, err := gc.PromQueryInstant(ds.ID, "count(up)", ts, "")
		if err != nil {
			t.Fatalf("PromQueryInstant with timestamp: %v", err)
		}
		if result == "(no results)" {
			t.Fatal("expected results for count(up) at -30m")
		}
	})

	t.Run("label_filtering", func(t *testing.T) {
		result, err := gc.PromQueryInstant(ds.ID, "up{job=\"kubernetes-nodes\"}", "", "")
		if err != nil {
			t.Fatalf("PromQueryInstant: %v", err)
		}
		// Compact output should hide noisy k8s labels
		if strings.Contains(result, "beta_kubernetes_io_") {
			t.Error("noisy label beta_kubernetes_io_ should be filtered in compact output")
		}
		if strings.Contains(result, "cloud_google_com_") {
			t.Error("noisy label cloud_google_com_ should be filtered in compact output")
		}
		// Should show the (+N labels) indicator
		if !strings.Contains(result, "(+") || !strings.Contains(result, "labels)") {
			t.Error("expected (+N labels) indicator for hidden labels")
		}
		// Important labels should still be present
		if !strings.Contains(result, "job=") {
			t.Error("expected 'job=' label in output")
		}
	})

	t.Run("no_results", func(t *testing.T) {
		result, err := gc.PromQueryInstant(ds.ID, "nonexistent_metric_xyz_12345", "", "")
		if err != nil {
			t.Fatalf("PromQueryInstant: %v", err)
		}
		if result != "(no results)" {
			t.Errorf("expected '(no results)', got: %s", truncate(result, 100))
		}
	})
}

func TestIntegrationPromQueryRange(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Prometheus", "prometheus")

	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix())
	end := fmt.Sprintf("%d", now.Unix())

	t.Run("basic_range", func(t *testing.T) {
		result, err := gc.PromQueryRange(ds.ID,
			`sum(rate(container_cpu_usage_seconds_total{namespace="default"}[5m])) by (pod)`,
			start, end, "5m", "")
		if err != nil {
			t.Fatalf("PromQueryRange: %v", err)
		}
		if result == "(no results)" {
			t.Skip("no CPU metrics in default namespace")
		}
		// Matrix output should have time-series sections
		if !strings.Contains(result, "──") {
			t.Error("expected series header (──) in matrix output")
		}
		if !strings.Contains(result, "TIME") {
			t.Error("expected TIME column header")
		}
		if !strings.Contains(result, "VALUE") {
			t.Error("expected VALUE column header")
		}
		if !strings.Contains(result, "samples") {
			t.Error("expected 'samples' count in series header")
		}
		t.Logf("result preview: %s", truncate(result, 300))
	})

	t.Run("defaults_without_params", func(t *testing.T) {
		// Empty start/end/step should default to 1h range, 60s step
		result, err := gc.PromQueryRange(ds.ID, "count(up)", "", "", "", "")
		if err != nil {
			t.Fatalf("PromQueryRange defaults: %v", err)
		}
		if result == "(no results)" {
			t.Fatal("expected results for count(up) with default range")
		}
	})
}

func TestIntegrationPromLabels(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Prometheus", "prometheus")

	result, err := gc.PromLabels(ds.ID)
	if err != nil {
		t.Fatalf("PromLabels: %v", err)
	}

	labels := strings.Split(strings.TrimSpace(result), "\n")
	if len(labels) < 10 {
		t.Fatalf("expected many labels, got %d", len(labels))
	}

	// Standard Prometheus labels should be present
	found := map[string]bool{}
	for _, l := range labels {
		found[l] = true
	}
	for _, expected := range []string{"__name__", "job", "instance", "namespace"} {
		if !found[expected] {
			t.Errorf("expected label %q not found", expected)
		}
	}

	t.Logf("found %d labels", len(labels))
}

func TestIntegrationPromLabelValues(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Prometheus", "prometheus")

	result, err := gc.PromLabelValues(ds.ID, "namespace")
	if err != nil {
		t.Fatalf("PromLabelValues: %v", err)
	}

	values := strings.Split(strings.TrimSpace(result), "\n")
	if len(values) == 0 {
		t.Fatal("expected at least one namespace value")
	}

	found := false
	for _, v := range values {
		if v == "default" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'default' namespace in label values")
	}

	t.Logf("found %d namespace values: %v", len(values), values)
}

func TestIntegrationPromSeries(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Prometheus", "prometheus")

	result, err := gc.PromSeries(ds.ID, `{__name__="up",job="kubernetes-nodes"}`)
	if err != nil {
		t.Fatalf("PromSeries: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) == 0 {
		t.Fatal("expected at least one series")
	}

	// Each line should contain the metric name and key labels
	for _, line := range lines[:min(3, len(lines))] {
		if !strings.Contains(line, "up") {
			t.Errorf("expected 'up' metric name in series line: %s", truncate(line, 100))
		}
		if !strings.Contains(line, "job=kubernetes-nodes") {
			t.Errorf("expected 'job=kubernetes-nodes' in series line: %s", truncate(line, 100))
		}
		// Compact filtering should be applied
		if strings.Contains(line, "beta_kubernetes_io_") {
			t.Error("noisy labels should be filtered in series output")
		}
	}

	t.Logf("found %d series", len(lines))
}

// ---------------------------------------------------------------------------
// Loki
// ---------------------------------------------------------------------------

func TestIntegrationLokiQuery(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Loki", "loki")

	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-30*time.Minute).UnixNano())
	end := fmt.Sprintf("%d", now.UnixNano())

	t.Run("basic_query", func(t *testing.T) {
		result, err := gc.LokiQuery(ds.ID, `{namespace="default"}`, start, end, 5, "", "")
		if err != nil {
			t.Fatalf("LokiQuery: %v", err)
		}
		if result == "(no log lines found)" {
			t.Skip("no logs in default namespace in last 30m")
		}
		// Should contain formatted log lines
		if !strings.Contains(result, "[") || !strings.Contains(result, "]") {
			t.Error("expected timestamped log lines with brackets")
		}
		if !strings.Contains(result, "log lines returned") {
			t.Error("expected summary line '--- N log lines returned ---'")
		}

		// Verify compact labels — should only show key labels
		lines := strings.Split(result, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "[") {
				if !strings.Contains(line, "namespace=") {
					t.Error("expected 'namespace=' in log line labels")
				}
				// Should NOT have filename (not in the compact label set)
				if strings.Contains(line, "filename=") {
					t.Error("filename should not appear in compact loki labels")
				}
				break
			}
		}

		t.Logf("result preview: %s", truncate(result, 300))
	})

	t.Run("with_filter", func(t *testing.T) {
		result, err := gc.LokiQuery(ds.ID, `{namespace="default"} |= "error"`, start, end, 3, "", "")
		if err != nil {
			t.Fatalf("LokiQuery with filter: %v", err)
		}
		// Either we get results containing "error" or no results
		if result != "(no log lines found)" {
			lines := strings.Split(result, "\n")
			foundError := false
			for _, line := range lines {
				if strings.HasPrefix(line, "[") && strings.Contains(strings.ToLower(line), "error") {
					foundError = true
					break
				}
			}
			if !foundError {
				t.Error("expected 'error' in filtered log lines")
			}
		}
	})

	t.Run("defaults_without_params", func(t *testing.T) {
		// Empty start/end should default to last 1h
		result, err := gc.LokiQuery(ds.ID, `{namespace="default"}`, "", "", 3, "", "")
		if err != nil {
			t.Fatalf("LokiQuery defaults: %v", err)
		}
		// Should return something (even if no results)
		if result == "" {
			t.Error("expected non-empty response")
		}
	})

	t.Run("limit_respected", func(t *testing.T) {
		result, err := gc.LokiQuery(ds.ID, `{namespace="default"}`, start, end, 2, "", "")
		if err != nil {
			t.Fatalf("LokiQuery: %v", err)
		}
		if result == "(no log lines found)" {
			t.Skip("no logs available")
		}
		// Count actual log lines (lines starting with [timestamp])
		count := 0
		for _, line := range strings.Split(result, "\n") {
			if strings.HasPrefix(line, "[") {
				count++
			}
		}
		if count > 2 {
			t.Errorf("expected at most 2 log lines, got %d", count)
		}
	})
}

func TestIntegrationLokiLabels(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Loki", "loki")

	result, err := gc.LokiLabels(ds.ID)
	if err != nil {
		t.Fatalf("LokiLabels: %v", err)
	}

	labels := strings.Split(strings.TrimSpace(result), "\n")
	if len(labels) < 3 {
		t.Fatalf("expected several labels, got %d", len(labels))
	}

	found := map[string]bool{}
	for _, l := range labels {
		found[l] = true
	}
	for _, expected := range []string{"namespace", "pod", "container"} {
		if !found[expected] {
			t.Errorf("expected label %q not found", expected)
		}
	}

	t.Logf("found %d loki labels", len(labels))
}

func TestIntegrationLokiLabelValues(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Loki", "loki")

	result, err := gc.LokiLabelValues(ds.ID, "namespace")
	if err != nil {
		t.Fatalf("LokiLabelValues: %v", err)
	}

	values := strings.Split(strings.TrimSpace(result), "\n")
	if len(values) == 0 {
		t.Fatal("expected at least one namespace value")
	}

	found := false
	for _, v := range values {
		if v == "default" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'default' namespace in loki label values")
	}

	t.Logf("found %d loki namespace values", len(values))
}

// ---------------------------------------------------------------------------
// Tempo
// ---------------------------------------------------------------------------

func TestIntegrationTempoSearch(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Tempo", "tempo")

	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix())
	end := fmt.Sprintf("%d", now.Unix())

	t.Run("basic_search", func(t *testing.T) {
		result, err := gc.TempoSearch(ds.ID, "", start, end, 5)
		if err != nil {
			t.Fatalf("TempoSearch: %v", err)
		}
		if result == "(no traces found)" {
			t.Skip("no traces in last hour")
		}
		// Table output should have headers
		if !strings.Contains(result, "TRACE_ID") {
			t.Error("expected TRACE_ID header")
		}
		if !strings.Contains(result, "SERVICE") {
			t.Error("expected SERVICE header")
		}
		if !strings.Contains(result, "DURATION") {
			t.Error("expected DURATION header")
		}
		if !strings.Contains(result, "traces ---") {
			t.Error("expected '--- N traces ---' summary")
		}

		t.Logf("result preview: %s", truncate(result, 400))
	})

	t.Run("with_query", func(t *testing.T) {
		result, err := gc.TempoSearch(ds.ID, "{}", start, end, 3)
		if err != nil {
			t.Fatalf("TempoSearch with query: %v", err)
		}
		// Should return valid output (either traces or no traces)
		if result == "" {
			t.Error("expected non-empty response")
		}
	})

	t.Run("defaults_without_params", func(t *testing.T) {
		result, err := gc.TempoSearch(ds.ID, "", "", "", 3)
		if err != nil {
			t.Fatalf("TempoSearch defaults: %v", err)
		}
		if result == "" {
			t.Error("expected non-empty response")
		}
	})
}

func TestIntegrationTempoTrace(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Tempo", "tempo")

	// First find a trace ID via search
	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix())
	end := fmt.Sprintf("%d", now.Unix())

	searchResult, err := gc.TempoSearch(ds.ID, "", start, end, 5)
	if err != nil {
		t.Fatalf("TempoSearch for trace IDs: %v", err)
	}
	if searchResult == "(no traces found)" {
		t.Skip("no traces available to fetch")
	}

	// Parse a trace ID from the search output (second line, first column)
	lines := strings.Split(strings.TrimSpace(searchResult), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least header + 1 trace, got %d lines", len(lines))
	}
	traceID := strings.Fields(lines[1])[0]
	if traceID == "" || traceID == "TRACE_ID" {
		t.Fatalf("could not parse trace ID from search output")
	}

	t.Run("fetch_trace", func(t *testing.T) {
		result, err := gc.TempoTrace(ds.ID, traceID)
		if err != nil {
			t.Fatalf("TempoTrace(%s): %v", traceID, err)
		}
		if result == "(no spans found in trace)" {
			t.Fatalf("expected spans in trace %s", traceID)
		}
		// Should contain span output
		if !strings.Contains(result, "span=") {
			t.Error("expected 'span=' in trace output")
		}
		if !strings.Contains(result, "spans ---") {
			t.Error("expected '--- N spans ---' summary")
		}
		// Should contain service names and status
		if !strings.Contains(result, "[OK/") && !strings.Contains(result, "[ERROR/") {
			t.Error("expected [OK/...] or [ERROR/...] status in span output")
		}
		// Should contain timestamps
		if !strings.Contains(result, "[") || !strings.Contains(result, "]") {
			t.Error("expected timestamps in brackets")
		}

		t.Logf("trace %s preview: %s", traceID, truncate(result, 400))
	})

	t.Run("nonexistent_trace", func(t *testing.T) {
		// Tempo returns 404 or empty for nonexistent traces
		_, err := gc.TempoTrace(ds.ID, "00000000000000000000000000000000")
		// Either an error or empty result is acceptable
		if err != nil {
			t.Logf("nonexistent trace returned error (expected): %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Google Cloud Monitoring
// ---------------------------------------------------------------------------

func TestIntegrationGCMProjects(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Google Cloud Monitoring", "stackdriver")

	result, err := gc.GCMProjects(ds.UID)
	if err != nil {
		t.Fatalf("GCMProjects: %v", err)
	}

	if result == "(no projects found)" {
		t.Fatal("expected at least one project")
	}
	if !strings.Contains(result, "PROJECT_ID") {
		t.Error("expected PROJECT_ID header")
	}
	if !strings.Contains(result, "time-entries-live") {
		t.Error("expected 'time-entries-live' project")
	}

	t.Logf("projects:\n%s", truncate(result, 500))
}

func TestIntegrationGCMQuery(t *testing.T) {
	gc := mustClient(t)
	ds := mustFindDatasource(t, gc, "Google Cloud Monitoring", "stackdriver")

	now := time.Now().UTC()
	start := fmt.Sprintf("%d", now.Add(-1*time.Hour).UnixMilli())
	end := fmt.Sprintf("%d", now.UnixMilli())

	t.Run("basic_query", func(t *testing.T) {
		result, err := gc.GCMQuery(ds.UID, "time-entries-live",
			"avg by (zone) (compute_googleapis_com:instance_cpu_utilization)",
			start, end, "300s", "")
		if err != nil {
			t.Fatalf("GCMQuery: %v", err)
		}
		if result == "(no results)" {
			t.Skip("no GCM data available")
		}
		// Matrix output should have series sections
		if !strings.Contains(result, "──") {
			t.Error("expected series header (──) in output")
		}
		if !strings.Contains(result, "TIME") {
			t.Error("expected TIME column header")
		}
		if !strings.Contains(result, "VALUE") {
			t.Error("expected VALUE column header")
		}
		if !strings.Contains(result, "samples") {
			t.Error("expected 'samples' count in series header")
		}
		t.Logf("result preview: %s", truncate(result, 400))
	})

	t.Run("tsv_format", func(t *testing.T) {
		result, err := gc.GCMQuery(ds.UID, "time-entries-live",
			"avg by (zone) (compute_googleapis_com:instance_cpu_utilization)",
			start, end, "300s", "tsv")
		if err != nil {
			t.Fatalf("GCMQuery tsv: %v", err)
		}
		if result == "(no results)" {
			t.Skip("no GCM data available")
		}
		// TSV should have tab-separated lines
		lines := strings.Split(strings.TrimSpace(result), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "#") {
				continue
			}
			if !strings.Contains(line, "\t") {
				t.Errorf("expected tab-separated line, got: %s", line)
				break
			}
		}
	})

	t.Run("bad_query", func(t *testing.T) {
		_, err := gc.GCMQuery(ds.UID, "time-entries-live",
			"bad_query{[", start, end, "60s", "")
		if err == nil {
			t.Error("expected error for bad query")
		}
	})

	t.Run("no_results", func(t *testing.T) {
		result, err := gc.GCMQuery(ds.UID, "time-entries-live",
			"nonexistent_metric_xyz_12345_gcm", start, end, "300s", "")
		if err != nil {
			// Some nonexistent metrics return errors, some return empty
			t.Logf("nonexistent metric returned error (expected): %v", err)
			return
		}
		if result != "(no results)" {
			t.Errorf("expected '(no results)', got: %s", truncate(result, 100))
		}
	})

	t.Run("defaults_without_params", func(t *testing.T) {
		// Empty start/end/step should default to 1h range, 60s step
		result, err := gc.GCMQuery(ds.UID, "time-entries-live",
			"count(compute_googleapis_com:instance_cpu_utilization)",
			"", "", "", "")
		if err != nil {
			t.Fatalf("GCMQuery defaults: %v", err)
		}
		if result == "(no results)" {
			t.Skip("no GCM data available")
		}
		if !strings.Contains(result, "──") {
			t.Error("expected series output with defaults")
		}
	})
}

// ---------------------------------------------------------------------------
// Helpers (unit-testable without Grafana)
// ---------------------------------------------------------------------------

func TestFormatLabels(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		result := formatLabels(map[string]string{})
		if result != "{}" {
			t.Errorf("expected '{}', got %q", result)
		}
	})

	t.Run("name_first", func(t *testing.T) {
		result := formatLabels(map[string]string{
			"__name__": "http_requests_total",
			"method":   "GET",
			"code":     "200",
		})
		if !strings.HasPrefix(result, "http_requests_total") {
			t.Errorf("expected __name__ first, got: %s", result)
		}
	})

	t.Run("filters_noisy_labels", func(t *testing.T) {
		result := formatLabels(map[string]string{
			"__name__":                           "up",
			"job":                                "test",
			"beta_kubernetes_io_arch":            "amd64",
			"cloud_google_com_gke_os_distribution": "cos",
			"topology_kubernetes_io_zone":        "us-central1-a",
		})
		if strings.Contains(result, "beta_kubernetes_io") {
			t.Error("should filter beta_kubernetes_io_ prefix")
		}
		if strings.Contains(result, "cloud_google_com") {
			t.Error("should filter cloud_google_com_ prefix")
		}
		if strings.Contains(result, "topology_kubernetes_io") {
			t.Error("should filter topology_kubernetes_io_ prefix")
		}
		if !strings.Contains(result, "job=test") {
			t.Error("should keep important label 'job'")
		}
		if !strings.Contains(result, "(+3 labels)") {
			t.Errorf("expected (+3 labels) indicator, got: %s", result)
		}
	})

	t.Run("keeps_important_labels", func(t *testing.T) {
		result := formatLabels(map[string]string{
			"namespace": "default",
			"pod":       "web-abc123",
			"container": "nginx",
			"cluster":   "prod",
		})
		for _, label := range []string{"namespace=default", "pod=web-abc123", "container=nginx", "cluster=prod"} {
			if !strings.Contains(result, label) {
				t.Errorf("expected %q in result: %s", label, result)
			}
		}
	})
}

func TestParseTimeNano(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := parseTimeNano(""); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("relative_to_nanos", func(t *testing.T) {
		result := parseTimeNano("1h")
		ts, err := strconv.ParseInt(result, 10, 64)
		if err != nil {
			t.Fatalf("expected nanosecond timestamp, got %q", result)
		}
		expected := time.Now().UTC().Add(-1 * time.Hour).UnixNano()
		diff := ts - expected
		if diff < -2e9 || diff > 2e9 {
			t.Errorf("nanosecond timestamp off by %dns", diff)
		}
	})

	t.Run("seconds_epoch_converted_to_nanos", func(t *testing.T) {
		result := parseTimeNano("1774452000")
		if result != "1774452000000000000" {
			t.Errorf("expected seconds→nanos conversion, got %q", result)
		}
	})

	t.Run("nanos_epoch_passthrough", func(t *testing.T) {
		input := "1774452000000000000"
		result := parseTimeNano(input)
		if result != input {
			t.Errorf("expected passthrough for nano timestamp, got %q", result)
		}
	})

	t.Run("short_number_converted", func(t *testing.T) {
		// 10-digit timestamp (seconds)
		result := parseTimeNano("1700000000")
		if result != "1700000000000000000" {
			t.Errorf("expected seconds→nanos, got %q", result)
		}
	})
}

func TestParseDurationSeconds(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"", -1},
		{"5", -1},
		{"30m", 1800},
		{"1h", 3600},
		{"6h", 21600},
		{"12h", 43200},
		{"1d", 86400},
		{"abc", -1},
		{"10x", -1},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseDurationSeconds(tt.input)
			if got != tt.expected {
				t.Errorf("parseDurationSeconds(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseRelativeTime(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := parseRelativeTime("", false); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("minutes_seconds", func(t *testing.T) {
		result := parseRelativeTime("30m", false)
		ts, err := strconv.ParseInt(result, 10, 64)
		if err != nil {
			t.Fatalf("expected unix timestamp, got %q", result)
		}
		expected := time.Now().UTC().Add(-30 * time.Minute).Unix()
		diff := ts - expected
		if diff < -2 || diff > 2 {
			t.Errorf("timestamp off by %ds", diff)
		}
	})

	t.Run("hours_seconds", func(t *testing.T) {
		result := parseRelativeTime("2h", false)
		ts, _ := strconv.ParseInt(result, 10, 64)
		expected := time.Now().UTC().Add(-2 * time.Hour).Unix()
		diff := ts - expected
		if diff < -2 || diff > 2 {
			t.Errorf("timestamp off by %ds", diff)
		}
	})

	t.Run("days_seconds", func(t *testing.T) {
		result := parseRelativeTime("1d", false)
		ts, _ := strconv.ParseInt(result, 10, 64)
		expected := time.Now().UTC().Add(-24 * time.Hour).Unix()
		diff := ts - expected
		if diff < -2 || diff > 2 {
			t.Errorf("timestamp off by %ds", diff)
		}
	})

	t.Run("nanoseconds", func(t *testing.T) {
		result := parseRelativeTime("1h", true)
		ts, err := strconv.ParseInt(result, 10, 64)
		if err != nil {
			t.Fatalf("expected nanosecond timestamp, got %q", result)
		}
		expected := time.Now().UTC().Add(-1 * time.Hour).UnixNano()
		diff := ts - expected
		// Allow 2 second tolerance in nanos
		if diff < -2e9 || diff > 2e9 {
			t.Errorf("nanosecond timestamp off by %dns", diff)
		}
	})

	t.Run("passthrough_unix_timestamp", func(t *testing.T) {
		if got := parseRelativeTime("1700000000", false); got != "1700000000" {
			t.Errorf("expected passthrough, got %q", got)
		}
	})

	t.Run("passthrough_unknown_unit", func(t *testing.T) {
		if got := parseRelativeTime("10x", false); got != "10x" {
			t.Errorf("expected passthrough for unknown unit, got %q", got)
		}
	})

	t.Run("single_char", func(t *testing.T) {
		// Single character should pass through (no num+unit to parse)
		if got := parseRelativeTime("5", false); got != "5" {
			t.Errorf("expected passthrough for single char, got %q", got)
		}
	})
}

func TestGCMIntervalMS(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
	}{
		{"", 60000},
		{"60s", 60000},
		{"300s", 300000},
		{"10s", 10000},
		{"5m", 300000},
		{"1h", 3600000},
		{"1d", 86400000},
		{"bad", 60000},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := gcmIntervalMS(tt.input)
			if got != tt.expected {
				t.Errorf("gcmIntervalMS(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractGCMError(t *testing.T) {
	t.Run("valid_error", func(t *testing.T) {
		input := `HTTP 400: {"results":{"A":{"error":"bad metric","status":500,"frames":[]}}}`
		got := extractGCMError(input)
		if got != "bad metric" {
			t.Errorf("expected 'bad metric', got %q", got)
		}
	})

	t.Run("no_json", func(t *testing.T) {
		got := extractGCMError("HTTP 500: Internal Server Error")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("no_error_field", func(t *testing.T) {
		input := `HTTP 200: {"results":{"A":{"status":200,"frames":[]}}}`
		got := extractGCMError(input)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		got := extractGCMError(`HTTP 400: {"results":{"A":{"error":"truncated...`)
		if got != "" {
			t.Errorf("expected empty for truncated JSON, got %q", got)
		}
	})
}

func TestFormatGCMResponseNulls(t *testing.T) {
	// Simulate a GCM response with null values
	body := `{
		"results": {
			"A": {
				"status": 200,
				"frames": [{
					"schema": {
						"refId": "A",
						"meta": {"custom": {"resultType": "matrix"}},
						"fields": [
							{"name": "Time", "type": "time"},
							{"name": "Value", "type": "number", "labels": {"zone": "us-central1-a"}}
						]
					},
					"data": {
						"values": [
							[1000, 2000, 3000, 4000],
							[1.5, null, 2.5, null]
						]
					}
				}]
			}
		}
	}`

	t.Run("table_skips_nulls", func(t *testing.T) {
		result, err := formatGCMResponse([]byte(body), "")
		if err != nil {
			t.Fatalf("formatGCMResponse: %v", err)
		}
		// Should show 2 samples (not 4)
		if !strings.Contains(result, "2 samples") {
			t.Errorf("expected '2 samples' (nulls excluded), got: %s", result)
		}
		// Should contain the non-null values
		if !strings.Contains(result, "1.5") || !strings.Contains(result, "2.5") {
			t.Errorf("expected non-null values in output: %s", result)
		}
	})

	t.Run("tsv_skips_nulls", func(t *testing.T) {
		result, err := formatGCMResponse([]byte(body), "tsv")
		if err != nil {
			t.Fatalf("formatGCMResponse tsv: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(result), "\n")
		// Should only have 2 data lines (nulls skipped)
		if len(lines) != 2 {
			t.Errorf("expected 2 TSV lines, got %d: %v", len(lines), lines)
		}
	})

	t.Run("empty_frames", func(t *testing.T) {
		emptyBody := `{"results": {"A": {"status": 200, "frames": []}}}`
		result, err := formatGCMResponse([]byte(emptyBody), "")
		if err != nil {
			t.Fatalf("formatGCMResponse empty: %v", err)
		}
		if result != "(no results)" {
			t.Errorf("expected '(no results)', got: %s", result)
		}
	})

	t.Run("error_in_result", func(t *testing.T) {
		errorBody := `{"results": {"A": {"status": 500, "error": "bad query", "frames": []}}}`
		_, err := formatGCMResponse([]byte(errorBody), "")
		if err == nil {
			t.Error("expected error for error result")
		}
		if !strings.Contains(err.Error(), "bad query") {
			t.Errorf("expected 'bad query' in error, got: %v", err)
		}
	})
}

func TestParseTimeMS(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := parseTimeMS(""); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("relative_to_ms", func(t *testing.T) {
		result := parseTimeMS("1h")
		ts, err := strconv.ParseInt(result, 10, 64)
		if err != nil {
			t.Fatalf("expected millisecond timestamp, got %q", result)
		}
		expected := time.Now().UTC().Add(-1 * time.Hour).UnixMilli()
		diff := ts - expected
		if diff < -2000 || diff > 2000 {
			t.Errorf("millisecond timestamp off by %dms", diff)
		}
	})

	t.Run("seconds_epoch_converted_to_ms", func(t *testing.T) {
		result := parseTimeMS("1774452000")
		if result != "1774452000000" {
			t.Errorf("expected seconds→ms conversion, got %q", result)
		}
	})

	t.Run("passthrough_ms_timestamp", func(t *testing.T) {
		// 13-digit ms timestamp should pass through
		input := "1774452000000"
		result := parseTimeMS(input)
		if result != input {
			t.Errorf("expected passthrough for ms timestamp, got %q", result)
		}
	})
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string: got %q", got)
	}
	if got := truncate("hello world", 5); got != "hello..." {
		t.Errorf("long string: got %q", got)
	}
	if got := truncate("", 5); got != "" {
		t.Errorf("empty string: got %q", got)
	}
}

func TestGetFlag(t *testing.T) {
	t.Run("extracts_flag", func(t *testing.T) {
		val, rest := getFlag([]string{"--start", "1h", "--end", "30m"}, "--start")
		if val != "1h" {
			t.Errorf("expected '1h', got %q", val)
		}
		if len(rest) != 2 || rest[0] != "--end" || rest[1] != "30m" {
			t.Errorf("unexpected rest: %v", rest)
		}
	})

	t.Run("extracts_equals_form", func(t *testing.T) {
		val, rest := getFlag([]string{"--limit=50", "other"}, "--limit")
		if val != "50" {
			t.Errorf("expected '50', got %q", val)
		}
		if len(rest) != 1 || rest[0] != "other" {
			t.Errorf("unexpected rest: %v", rest)
		}
	})

	t.Run("missing_flag", func(t *testing.T) {
		val, rest := getFlag([]string{"--start", "1h"}, "--end")
		if val != "" {
			t.Errorf("expected empty, got %q", val)
		}
		if len(rest) != 2 {
			t.Errorf("expected unchanged args, got: %v", rest)
		}
	})

	t.Run("no_mutation_of_original_args", func(t *testing.T) {
		// This was the original bug: append mutated the shared backing array
		original := []string{"--start", "AAA", "--end", "BBB", "--limit", "5"}
		startVal, remaining := getFlag(original, "--start")
		endVal, remaining := getFlag(remaining, "--end")
		limitVal, _ := getFlag(remaining, "--limit")

		if startVal != "AAA" {
			t.Errorf("start: expected 'AAA', got %q", startVal)
		}
		if endVal != "BBB" {
			t.Errorf("end: expected 'BBB', got %q", endVal)
		}
		if limitVal != "5" {
			t.Errorf("limit: expected '5', got %q", limitVal)
		}
	})

	t.Run("multiple_flags_interleaved", func(t *testing.T) {
		args := []string{"--query", "{}", "--start", "1h", "--end", "30m", "--limit", "10"}
		query, args := getFlag(args, "--query")
		start, args := getFlag(args, "--start")
		end, args := getFlag(args, "--end")
		limit, args := getFlag(args, "--limit")

		if query != "{}" {
			t.Errorf("query: expected '{}', got %q", query)
		}
		if start != "1h" {
			t.Errorf("start: expected '1h', got %q", start)
		}
		if end != "30m" {
			t.Errorf("end: expected '30m', got %q", end)
		}
		if limit != "10" {
			t.Errorf("limit: expected '10', got %q", limit)
		}
		if len(args) != 0 {
			t.Errorf("expected empty remaining args, got: %v", args)
		}
	})
}

func TestIsNoisyLabel(t *testing.T) {
	noisy := []string{
		"addon_gke_io_foo",
		"beta_kubernetes_io_arch",
		"cloud_google_com_gke_nodepool",
		"topology_kubernetes_io_zone",
	}
	for _, label := range noisy {
		if !isNoisyLabel(label) {
			t.Errorf("expected %q to be noisy", label)
		}
	}

	clean := []string{
		"job",
		"namespace",
		"pod",
		"custom_label",
		"http_requests_total",
	}
	for _, label := range clean {
		if isNoisyLabel(label) {
			t.Errorf("expected %q to NOT be noisy", label)
		}
	}
}

// ---------------------------------------------------------------------------
// CLI binary (end-to-end via exec)
// ---------------------------------------------------------------------------

func TestIntegrationCLIEndToEnd(t *testing.T) {
	skipIfNoGrafana(t)

	// Build the binary
	binPath := t.TempDir() + "/grafana-cli"
	cmd := newCmd("go", "build", "-o", binPath, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %s\n%s", err, out)
	}

	env := append(os.Environ(),
		"GRAFANA_URL="+os.Getenv("GRAFANA_URL"),
		"GRAFANA_TOKEN="+os.Getenv("GRAFANA_TOKEN"),
	)

	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, binPath, args...)
		c.Env = env
		out, err := c.CombinedOutput()
		return string(out), err
	}

	t.Run("help", func(t *testing.T) {
		// --help exits with 1 (usage) but should print usage text
		out, _ := run("--help")
		if !strings.Contains(out, "grafana-cli") {
			t.Error("expected usage text")
		}
	})

	t.Run("datasources", func(t *testing.T) {
		out, err := run("datasources")
		if err != nil {
			t.Fatalf("datasources: %v\n%s", err, out)
		}
		if !strings.Contains(out, "prometheus") {
			t.Error("expected prometheus in datasources output")
		}
	})

	t.Run("ds_alias", func(t *testing.T) {
		out, err := run("ds")
		if err != nil {
			t.Fatalf("ds: %v\n%s", err, out)
		}
		if !strings.Contains(out, "ID") || !strings.Contains(out, "NAME") {
			t.Error("expected table headers")
		}
	})

	t.Run("prom_query", func(t *testing.T) {
		out, err := run("prom", "query", "Prometheus", "count(up)")
		if err != nil {
			t.Fatalf("prom query: %v\n%s", err, out)
		}
		if !strings.Contains(out, "VALUE") {
			t.Error("expected VALUE header")
		}
	})

	t.Run("prom_query_range_with_flags", func(t *testing.T) {
		out, err := run("prom", "query-range", "Prometheus", "count(up)", "--start", "1h", "--step", "15m")
		if err != nil {
			t.Fatalf("prom query-range: %v\n%s", err, out)
		}
		if !strings.Contains(out, "TIME") || !strings.Contains(out, "VALUE") {
			t.Error("expected TIME/VALUE headers")
		}
	})

	t.Run("prom_labels", func(t *testing.T) {
		out, err := run("prom", "labels", "Prometheus")
		if err != nil {
			t.Fatalf("prom labels: %v\n%s", err, out)
		}
		if !strings.Contains(out, "__name__") {
			t.Error("expected __name__ in labels")
		}
	})

	t.Run("prom_label_values", func(t *testing.T) {
		out, err := run("prom", "label-values", "Prometheus", "namespace")
		if err != nil {
			t.Fatalf("prom label-values: %v\n%s", err, out)
		}
		if !strings.Contains(out, "default") {
			t.Error("expected 'default' namespace")
		}
	})

	t.Run("loki_query", func(t *testing.T) {
		out, err := run("loki", "query", "Loki", `{namespace="default"}`, "--start", "30m", "--limit", "3")
		if err != nil {
			t.Fatalf("loki query: %v\n%s", err, out)
		}
		if !strings.Contains(out, "[") {
			t.Error("expected timestamped log lines")
		}
	})

	t.Run("loki_labels", func(t *testing.T) {
		out, err := run("loki", "labels", "Loki")
		if err != nil {
			t.Fatalf("loki labels: %v\n%s", err, out)
		}
		if !strings.Contains(out, "namespace") {
			t.Error("expected 'namespace' label")
		}
	})

	t.Run("loki_label_values", func(t *testing.T) {
		out, err := run("loki", "label-values", "Loki", "namespace")
		if err != nil {
			t.Fatalf("loki label-values: %v\n%s", err, out)
		}
		if !strings.Contains(out, "default") {
			t.Error("expected 'default' namespace")
		}
	})

	t.Run("tempo_search", func(t *testing.T) {
		out, err := run("tempo", "search", "Tempo", "--start", "1h", "--limit", "3")
		if err != nil {
			t.Fatalf("tempo search: %v\n%s", err, out)
		}
		if !strings.Contains(out, "TRACE_ID") && !strings.Contains(out, "no traces") {
			t.Error("expected TRACE_ID header or 'no traces' message")
		}
	})

	t.Run("gcm_projects", func(t *testing.T) {
		out, err := run("gcm", "projects", "Google Cloud Monitoring")
		if err != nil {
			t.Fatalf("gcm projects: %v\n%s", err, out)
		}
		if !strings.Contains(out, "PROJECT_ID") {
			t.Error("expected PROJECT_ID header")
		}
	})

	t.Run("gcm_query", func(t *testing.T) {
		out, err := run("gcm", "query", "Google Cloud Monitoring",
			"count(compute_googleapis_com:instance_cpu_utilization)",
			"--project", "time-entries-live", "--start", "1h", "--step", "300s")
		if err != nil {
			t.Fatalf("gcm query: %v\n%s", err, out)
		}
		if !strings.Contains(out, "──") && !strings.Contains(out, "no results") {
			t.Error("expected series output or 'no results'")
		}
	})

	t.Run("gcm_query_missing_project", func(t *testing.T) {
		_, err := run("gcm", "query", "Google Cloud Monitoring", "up")
		if err == nil {
			t.Error("expected error when --project is missing")
		}
	})

	t.Run("unknown_command", func(t *testing.T) {
		_, err := run("bogus")
		if err == nil {
			t.Error("expected error for unknown command")
		}
	})

	t.Run("missing_env", func(t *testing.T) {
		c := newCmd(binPath, "datasources")
		c.Env = []string{} // no GRAFANA_URL/TOKEN
		_, err := c.CombinedOutput()
		if err == nil {
			t.Error("expected error when env vars are missing")
		}
	})
}

// newCmd creates an exec.Cmd. Extracted to keep import in one place.
func newCmd(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}


