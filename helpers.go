package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// noisyLabelPrefixes are label prefixes commonly found in Kubernetes/cloud
// environments that add clutter without useful signal for most queries.
var noisyLabelPrefixes = []string{
	"addon_gke_io_",
	"annotation_",
	"beta_kubernetes_io_",
	"cloud_google_com_",
	"disk_type_gke_io_",
	"failure_domain_beta_kubernetes_io_",
	"iam_gke_io_",
	"kubernetes_io_",
	"node_kubernetes_io_",
	"topology_gke_io_",
	"topology_kubernetes_io_",
}

// importantLabels are labels that are always shown in compact output.
var importantLabels = map[string]bool{
	"__name__": true, "job": true, "instance": true,
	"namespace": true, "pod": true, "container": true,
	"service": true, "service_name": true, "component": true,
	"cluster": true, "node": true, "deployment": true,
	"statefulset": true, "daemonset": true, "app": true,
	"name": true, "image": true, "cpu": true,
	"device": true, "endpoint": true, "method": true,
	"path": true, "code": true, "status": true,
	"le": true, "quantile": true, "reason": true,
	"type": true, "mode": true, "phase": true,
	"resource": true, "verb": true, "scope": true,
	"prometheus_cluster": true,
}

func isNoisyLabel(key string) bool {
	for _, prefix := range noisyLabelPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func formatLabels(m map[string]string) string {
	return formatLabelsFiltered(m, true)
}

func formatLabelsFiltered(m map[string]string, compact bool) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(m))
	if name, ok := m["__name__"]; ok {
		parts = append(parts, name)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	hidden := 0
	for _, k := range keys {
		if compact && !importantLabels[k] && isNoisyLabel(k) {
			hidden++
			continue
		}
		parts = append(parts, k+"="+m[k])
	}
	if hidden > 0 {
		parts = append(parts, fmt.Sprintf("(+%d labels)", hidden))
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// parseRelativeTime parses a relative duration string (e.g. "1h", "30m", "2d")
// into an absolute timestamp. If nano is true, returns nanosecond epoch; otherwise seconds.
// Non-relative values are returned as-is.
func parseRelativeTime(val string, nano bool) string {
	if val == "" {
		return ""
	}
	if len(val) > 1 {
		numStr := val[:len(val)-1]
		unit := val[len(val)-1]
		if num, err := strconv.Atoi(numStr); err == nil {
			now := time.Now().UTC()
			var d time.Duration
			switch unit {
			case 'm':
				d = time.Duration(num) * time.Minute
			case 'h':
				d = time.Duration(num) * time.Hour
			case 'd':
				d = time.Duration(num) * 24 * time.Hour
			default:
				return val
			}
			t := now.Add(-d)
			if nano {
				return fmt.Sprintf("%d", t.UnixNano())
			}
			return fmt.Sprintf("%d", t.Unix())
		}
	}
	return val
}

func parseTimeFlag(val string) string {
	return parseRelativeTime(val, false)
}

func parseTimeNano(val string) string {
	if val == "" {
		return ""
	}
	// Try relative time (e.g., "1h", "30m", "2d")
	rel := parseRelativeTime(val, true)
	if rel != val {
		return rel
	}
	// Auto-detect seconds-epoch timestamps (10-12 digits) and convert to
	// nanoseconds. Nanosecond timestamps have 19 digits and pass through.
	if ts, err := strconv.ParseInt(val, 10, 64); err == nil && len(val) <= 12 {
		return fmt.Sprintf("%d", ts*1_000_000_000)
	}
	return val
}

// parseTimeMS converts a time flag to a millisecond epoch string.
// Handles relative times (1h, 30m, 2d) and unix second timestamps.
func parseTimeMS(val string) string {
	if val == "" {
		return ""
	}
	// Try relative time (e.g., "1h", "30m", "2d") → seconds → ms
	rel := parseRelativeTime(val, false)
	if rel != val {
		if ts, err := strconv.ParseInt(rel, 10, 64); err == nil {
			return fmt.Sprintf("%d", ts*1000)
		}
	}
	// Auto-detect seconds-epoch timestamps (10-12 digits) and convert to ms.
	if ts, err := strconv.ParseInt(val, 10, 64); err == nil && len(val) <= 12 {
		return fmt.Sprintf("%d", ts*1000)
	}
	return val
}

// parseDurationSeconds returns the number of seconds represented by a relative
// duration string (e.g. "30m" → 1800, "2h" → 7200). Returns -1 if the value
// is not a recognized relative duration.
func parseDurationSeconds(val string) int64 {
	if val == "" || len(val) < 2 {
		return -1
	}
	numStr := val[:len(val)-1]
	unit := val[len(val)-1]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return -1
	}
	switch unit {
	case 'm':
		return int64(num) * 60
	case 'h':
		return int64(num) * 3600
	case 'd':
		return int64(num) * 86400
	default:
		return -1
	}
}
