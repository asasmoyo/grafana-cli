package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// hideLabelPrefixesEnv overrides which label prefixes are hidden from compact
// output. Set it to a comma-separated list to replace the defaults, or to an
// empty value to keep every label:
//
//	GRAFANA_HIDE_LABEL_PREFIXES="my_internal_,ec2_tag_"
//	GRAFANA_HIDE_LABEL_PREFIXES=""
const hideLabelPrefixesEnv = "GRAFANA_HIDE_LABEL_PREFIXES"

// defaultNoisyLabelPrefixes are hidden unless overridden. The defaults target
// Kubernetes and GKE, by far the most common source of label noise, but they
// are only a default: no deployment is assumed.
var defaultNoisyLabelPrefixes = []string{
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

// importantLabels are never hidden, even when they match a hidden prefix. This
// list can only rescue a label, never suppress one, so it stays harmless on
// deployments that use none of these names.
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

// hiddenLabelPrefixes returns the prefixes to hide, honouring the environment
// override. Read per formatting call rather than cached so that the behaviour
// is testable and a long-running process would pick up a change.
func hiddenLabelPrefixes() []string {
	raw, ok := os.LookupEnv(hideLabelPrefixesEnv)
	if !ok {
		return defaultNoisyLabelPrefixes
	}
	var prefixes []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	return prefixes
}

func hasAnyPrefix(key string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func isNoisyLabel(key string) bool {
	return hasAnyPrefix(key, hiddenLabelPrefixes())
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

	noisy := hiddenLabelPrefixes()
	hidden := 0
	for _, k := range keys {
		if compact && !importantLabels[k] && hasAnyPrefix(k, noisy) {
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

// parseDurationString parses a lookback duration such as "90s", "30m", "1h",
// "1h30m", "2d" or "1w". Compound values are supported.
//
// A bare number is deliberately rejected: it is indistinguishable from a unix
// timestamp, and guessing wrong silently shifts every query window.
func parseDurationString(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	var total time.Duration
	for i := 0; i < len(s); {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start || i == len(s) {
			return 0, false // missing number, or trailing number without a unit
		}
		num, err := strconv.Atoi(s[start:i])
		if err != nil {
			return 0, false
		}
		var unit time.Duration
		switch s[i] {
		case 's':
			unit = time.Second
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		case 'd':
			unit = 24 * time.Hour
		case 'w':
			unit = 7 * 24 * time.Hour
		default:
			return 0, false
		}
		total += time.Duration(num) * unit
		i++
	}
	return total, true
}

// isUnixTimestamp reports whether val is a bare (all digit) epoch value.
func isUnixTimestamp(val string) bool {
	if val == "" {
		return false
	}
	for i := 0; i < len(val); i++ {
		if val[i] < '0' || val[i] > '9' {
			return false
		}
	}
	return true
}

// parseAbsoluteTime parses an RFC3339 instant, with or without fractional
// seconds and with any UTC offset.
//
// Both validateTimeArg and the parseTime* converters go through this function:
// if validation accepts a spelling that conversion does not understand, the
// value reaches the datasource unconverted, which is the one failure the
// validator exists to prevent.
func parseAbsoluteTime(val string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// validateTimeArg rejects --start/--end values that are neither a relative
// duration, a unix timestamp nor an RFC3339 instant. Without this check an
// unsupported spelling such as "90sec" or "1hour" is forwarded verbatim to the
// datasource, which answers with an opaque parse error.
func validateTimeArg(flagName, val string) error {
	if val == "" {
		return nil
	}
	if _, ok := parseDurationString(val); ok {
		return nil
	}
	if isUnixTimestamp(val) {
		return nil
	}
	if _, ok := parseAbsoluteTime(val); ok {
		return nil
	}
	return fmt.Errorf("invalid %s value %q: expected a relative duration (90s, 30m, 1h, 1h30m, 2d, 1w), a unix timestamp, or an RFC3339 time", flagName, val)
}

// validateStepArg rejects --step values that no datasource would accept. Bare
// numbers are allowed here (Prometheus reads them as seconds) since a step is
// never a timestamp.
func validateStepArg(flagName, val string) error {
	if val == "" {
		return nil
	}
	if _, ok := parseDurationString(val); ok {
		return nil
	}
	if isUnixTimestamp(val) {
		return nil
	}
	return fmt.Errorf("invalid %s value %q: expected a duration such as 15s, 60s, 5m, 1h", flagName, val)
}

// parseRelativeTime resolves a relative duration string (e.g. "1h", "30m",
// "2d") into an absolute timestamp. If nano is true the result is a nanosecond
// epoch, otherwise seconds. Values that are not relative durations are returned
// unchanged.
func parseRelativeTime(val string, nano bool) string {
	d, ok := parseDurationString(val)
	if !ok {
		return val
	}
	t := time.Now().UTC().Add(-d)
	if nano {
		return fmt.Sprintf("%d", t.UnixNano())
	}
	return fmt.Sprintf("%d", t.Unix())
}

// parseTimeFlag converts a time argument to a unix second epoch, the unit used
// by the Prometheus and Tempo APIs.
func parseTimeFlag(val string) string {
	if t, ok := parseAbsoluteTime(val); ok {
		return strconv.FormatInt(t.Unix(), 10)
	}
	return parseRelativeTime(val, false)
}

// parseTimeNano converts a time argument to a nanosecond epoch, the unit used
// by the Loki API.
func parseTimeNano(val string) string {
	if val == "" {
		return ""
	}
	if t, ok := parseAbsoluteTime(val); ok {
		return strconv.FormatInt(t.UnixNano(), 10)
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

// parseTimeMS converts a time argument to a millisecond epoch, the unit
// Grafana's /api/ds/query expects in "from"/"to".
func parseTimeMS(val string) string {
	if val == "" {
		return ""
	}
	if t, ok := parseAbsoluteTime(val); ok {
		return strconv.FormatInt(t.UnixMilli(), 10)
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
	d, ok := parseDurationString(val)
	if !ok {
		return -1
	}
	return int64(d / time.Second)
}
