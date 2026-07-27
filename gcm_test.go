package main

import (
	"strings"
	"testing"
)

// Unit tests for the Google Cloud Monitoring request/response plumbing.

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
