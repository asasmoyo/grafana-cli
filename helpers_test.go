package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Unit tests for the label, time and duration helpers. These have no external
// dependency and therefore always run — they used to live in
// integration_test.go, where it was impossible to tell them apart from the
// tests that silently skip without a live Grafana.

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
			"__name__":                             "up",
			"job":                                  "test",
			"beta_kubernetes_io_arch":              "amd64",
			"cloud_google_com_gke_os_distribution": "cos",
			"topology_kubernetes_io_zone":          "us-central1-a",
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

// ---------------------------------------------------------------------------
// Deployment neutrality
// ---------------------------------------------------------------------------

// The label filter must not assume Kubernetes. Its defaults target it, but any
// deployment can replace or disable them.
func TestHiddenLabelPrefixesAreConfigurable(t *testing.T) {
	labels := map[string]string{
		"__name__": "cpu_usage", "host": "db-01",
		"kubernetes_io_arch": "amd64", "ec2_tag_owner": "platform",
	}

	t.Run("defaults hide kubernetes noise", func(t *testing.T) {
		got := formatLabels(labels)
		if strings.Contains(got, "kubernetes_io_arch") {
			t.Errorf("expected kubernetes_io_arch to be hidden by default: %s", got)
		}
		if !strings.Contains(got, "ec2_tag_owner") {
			t.Errorf("ec2_tag_owner is not noise by default: %s", got)
		}
	})

	t.Run("override replaces the defaults", func(t *testing.T) {
		t.Setenv(hideLabelPrefixesEnv, "ec2_tag_")
		got := formatLabels(labels)
		if strings.Contains(got, "ec2_tag_owner") {
			t.Errorf("expected ec2_tag_owner to be hidden: %s", got)
		}
		if !strings.Contains(got, "kubernetes_io_arch") {
			t.Errorf("the override replaces the defaults, so kubernetes_io_arch should show: %s", got)
		}
	})

	t.Run("empty override keeps every label", func(t *testing.T) {
		t.Setenv(hideLabelPrefixesEnv, "")
		got := formatLabels(labels)
		for k := range labels {
			if k != "__name__" && !strings.Contains(got, k) {
				t.Errorf("expected %q to be kept: %s", k, got)
			}
		}
	})
}

// A Loki keyed by something other than Kubernetes labels must still identify
// its streams: this used to render every line as "{}".
func TestStreamLabelsWithoutKubernetes(t *testing.T) {
	tests := []struct {
		name   string
		stream map[string]string
		want   []string
	}{
		{
			name:   "kubernetes labels are preferred",
			stream: map[string]string{"namespace": "prod", "pod": "api-0", "host": "node-1"},
			want:   []string{"namespace=prod", "pod=api-0"},
		},
		{
			name:   "unknown scheme falls back to the stream's own labels",
			stream: map[string]string{"host": "web-01", "filename": "/var/log/nginx/error.log"},
			want:   []string{"host=web-01", "filename=/var/log/nginx/error.log"},
		},
		{
			name:   "systemd style",
			stream: map[string]string{"unit": "sshd.service", "hostname": "bastion"},
			want:   []string{"unit=sshd.service", "hostname=bastion"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStreamLabels(tt.stream)
			if got == "" {
				t.Fatal("stream labels rendered empty")
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in %q", want, got)
				}
			}
		})
	}

	t.Run("wide streams are capped", func(t *testing.T) {
		stream := map[string]string{}
		for i := 0; i < 10; i++ {
			stream[fmt.Sprintf("label_%02d", i)] = "v"
		}
		got := formatStreamLabels(stream)
		if !strings.Contains(got, "(+6 labels)") {
			t.Errorf("expected a hidden-label count, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Absolute (RFC3339) time arguments
// ---------------------------------------------------------------------------

// RFC3339 is accepted by the validator, so every converter must turn it into
// its own epoch unit. Passing it through unconverted worked only for the
// backends that happen to parse RFC3339 themselves (Prometheus, Loki) and
// silently produced a bad window on the ones that do not (Tempo, GCM).
func TestParseTimeRFC3339(t *testing.T) {
	// All fixtures denote the same instant: 2026-07-27T10:00:00Z == 1785146400.
	tests := []struct {
		name                    string
		in                      string
		wantSec, wantMS, wantNS string
	}{
		{
			name:    "utc",
			in:      "2026-07-27T10:00:00Z",
			wantSec: "1785146400", wantMS: "1785146400000", wantNS: "1785146400000000000",
		},
		{
			// Same instant expressed in another zone must convert identically.
			name:    "positive offset",
			in:      "2026-07-27T12:00:00+02:00",
			wantSec: "1785146400", wantMS: "1785146400000", wantNS: "1785146400000000000",
		},
		{
			name:    "negative offset",
			in:      "2026-07-27T05:00:00-05:00",
			wantSec: "1785146400", wantMS: "1785146400000", wantNS: "1785146400000000000",
		},
		{
			name:    "fractional seconds",
			in:      "2026-07-27T10:00:00.123456789Z",
			wantSec: "1785146400", wantMS: "1785146400123", wantNS: "1785146400123456789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseTimeFlag(tt.in); got != tt.wantSec {
				t.Errorf("parseTimeFlag(%q) = %q, want %q (unix seconds)", tt.in, got, tt.wantSec)
			}
			if got := parseTimeMS(tt.in); got != tt.wantMS {
				t.Errorf("parseTimeMS(%q) = %q, want %q (unix millis)", tt.in, got, tt.wantMS)
			}
			if got := parseTimeNano(tt.in); got != tt.wantNS {
				t.Errorf("parseTimeNano(%q) = %q, want %q (unix nanos)", tt.in, got, tt.wantNS)
			}
		})
	}
}

// The property that prevents this class of bug returning: anything the
// validator accepts must be converted to a bare epoch by every converter, so
// validation and conversion cannot drift apart again.
func TestAcceptedTimeArgsAreAlwaysConverted(t *testing.T) {
	accepted := []string{
		"90s", "30m", "1h", "1h30m", "2d", "1w",
		"1785146400", "1785146400000", "1785146400000000000",
		"2026-07-27T10:00:00Z", "2026-07-27T12:00:00+02:00", "2026-07-27T10:00:00.5Z",
	}

	converters := map[string]func(string) string{
		"parseTimeFlag": parseTimeFlag,
		"parseTimeNano": parseTimeNano,
		"parseTimeMS":   parseTimeMS,
	}

	for _, val := range accepted {
		if err := validateTimeArg("--start", val); err != nil {
			t.Fatalf("fixture %q is not accepted by the validator: %v", val, err)
		}
		for name, convert := range converters {
			got := convert(val)
			if !isUnixTimestamp(got) {
				t.Errorf("%s(%q) = %q, which is not a bare epoch: the datasource would receive it unconverted", name, val, got)
			}
		}
	}
}
