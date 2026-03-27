package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// --- Google Cloud Monitoring queries ---

func (g *GrafanaClient) GCMQuery(dsUID, project, expr, start, end, step, format string) (string, error) {
	now := time.Now().UTC()
	if end == "" {
		end = fmt.Sprintf("%d", now.UnixMilli())
	}
	if start == "" {
		start = fmt.Sprintf("%d", now.Add(-1*time.Hour).UnixMilli())
	}
	if step == "" {
		step = defaultStep
	}

	query := map[string]interface{}{
		"queries": []map[string]interface{}{{
			"datasource": map[string]string{"uid": dsUID},
			"refId":      "A",
			"queryType":  "promQL",
			// timeSeriesList stub is required — without it Grafana's migration
			// code treats the request as a legacy query and crashes.
			"timeSeriesList": map[string]interface{}{
				"projectName": "",
				"filters":     []string{},
				"view":        "FULL",
			},
			"promQLQuery": map[string]string{
				"projectName": project,
				"expr":        expr,
				"step":        step,
			},
			"intervalMs":    gcmIntervalMS(step),
			"maxDataPoints": 1000,
		}},
		"from": start,
		"to":   end,
	}

	body, err := json.Marshal(query)
	if err != nil {
		return "", fmt.Errorf("building GCM query: %w", err)
	}

	respBody, err := g.post("/api/ds/query", body)
	if err != nil {
		// /api/ds/query returns HTTP 400 for invalid queries but the body
		// still contains a structured error. Try to extract a clean message.
		if msg := extractGCMError(err.Error()); msg != "" {
			return "", fmt.Errorf("GCM query failed: %s", msg)
		}
		return "", err
	}

	return formatGCMResponse(respBody, format)
}

func (g *GrafanaClient) GCMProjects(dsUID string) (string, error) {
	body, err := g.resourceGet(dsUID, "projects")
	if err != nil {
		return "", err
	}

	var projects []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal(body, &projects); err != nil {
		return "", fmt.Errorf("parsing GCM projects: %w", err)
	}

	if len(projects) == 0 {
		return "(no projects found)", nil
	}

	var sb strings.Builder
	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "PROJECT_ID\tLABEL\n")
	for _, p := range projects {
		fmt.Fprintf(w, "%s\t%s\n", p.Value, p.Label)
	}
	w.Flush()
	return sb.String(), nil
}

// --- GCM response formatter ---

// gcmDsQueryResponse represents the Grafana /api/ds/query response envelope.
type gcmDsQueryResponse struct {
	Results map[string]gcmQueryResult `json:"results"`
}

type gcmQueryResult struct {
	Status int             `json:"status"`
	Error  string          `json:"error"`
	Frames []gcmDataFrame  `json:"frames"`
}

type gcmDataFrame struct {
	Schema gcmFrameSchema `json:"schema"`
	Data   gcmFrameData   `json:"data"`
}

type gcmFrameSchema struct {
	RefID  string         `json:"refId"`
	Meta   gcmFrameMeta   `json:"meta"`
	Fields []gcmField     `json:"fields"`
}

type gcmFrameMeta struct {
	Custom map[string]interface{} `json:"custom"`
}

type gcmField struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
}

type gcmFrameData struct {
	Values []json.RawMessage `json:"values"`
}

func formatGCMResponse(body []byte, format string) (string, error) {
	var resp gcmDsQueryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing GCM response: %w", err)
	}

	result, ok := resp.Results["A"]
	if !ok {
		return "", fmt.Errorf("no result for refId 'A' in GCM response")
	}

	if result.Error != "" {
		return "", fmt.Errorf("GCM query failed: %s", result.Error)
	}

	if len(result.Frames) == 0 {
		return "(no results)", nil
	}

	var sb strings.Builder

	for i, frame := range result.Frames {
		// Each frame has 2 fields: Time (index 0), Value (index 1)
		if len(frame.Schema.Fields) < 2 || len(frame.Data.Values) < 2 {
			continue
		}

		valueField := frame.Schema.Fields[1]
		labels := formatLabels(valueField.Labels)

		// Parse timestamps (millisecond epoch)
		var timestamps []float64
		if err := json.Unmarshal(frame.Data.Values[0], &timestamps); err != nil {
			return "", fmt.Errorf("parsing timestamps: %w", err)
		}
		// Parse values — use *float64 so JSON null becomes nil
		// (GCM returns null for time points where a metric is absent)
		var values []*float64
		if err := json.Unmarshal(frame.Data.Values[1], &values); err != nil {
			return "", fmt.Errorf("parsing values: %w", err)
		}

		if format == "tsv" {
			if len(result.Frames) > 1 {
				fmt.Fprintf(&sb, "# %s\n", labels)
			}
			for j := range timestamps {
				if j >= len(values) {
					break
				}
				if values[j] == nil {
					continue // skip null data points
				}
				t := time.UnixMilli(int64(timestamps[j])).UTC().Format("2006-01-02T15:04:05Z")
				fmt.Fprintf(&sb, "%s\t%g\n", t, *values[j])
			}
		} else {
			if i >= 50 {
				fmt.Fprintf(&sb, "\n... (%d more series truncated)\n", len(result.Frames)-50)
				break
			}
			nonNull := 0
			for _, v := range values {
				if v != nil {
					nonNull++
				}
			}
			fmt.Fprintf(&sb, "── %s (%d samples) ──\n", labels, nonNull)
			w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "TIME\tVALUE\n")
			// Show at most 50 samples, evenly distributed
			sampleStep := 1
			if len(values) > 50 {
				sampleStep = len(values) / 50
			}
			for j := 0; j < len(timestamps) && j < len(values); j += sampleStep {
				if values[j] == nil {
					continue // skip null data points
				}
				t := time.UnixMilli(int64(timestamps[j])).UTC().Format("2006-01-02 15:04:05")
				fmt.Fprintf(w, "%s\t%g\n", t, *values[j])
			}
			w.Flush()
			sb.WriteString("\n")
		}
	}

	out := sb.String()
	if out == "" {
		return "(no results)", nil
	}
	return out, nil
}

// extractGCMError tries to extract a readable error message from a Grafana
// /api/ds/query error response embedded in an HTTP error string.
// Note: post() truncates the body to 500 bytes; if the error message is
// very long the JSON may be invalid and this function returns "".
func extractGCMError(errStr string) string {
	// The error from post() looks like: "HTTP 400: {\"results\":{\"A\":{\"error\":\"...\", ...}}}"
	idx := strings.Index(errStr, "{")
	if idx < 0 {
		return ""
	}
	jsonStr := errStr[idx:]
	var resp gcmDsQueryResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return ""
	}
	if r, ok := resp.Results["A"]; ok && r.Error != "" {
		return r.Error
	}
	return ""
}

// gcmIntervalMS converts a step string (e.g. "60s", "5m") to an intervalMs
// value for the Grafana query. Falls back to 60000 (60s) if unparseable.
func gcmIntervalMS(step string) int64 {
	if step == "" {
		return 60000
	}
	// parseDurationSeconds handles "m", "h", "d"
	if secs := parseDurationSeconds(step); secs > 0 {
		return secs * 1000
	}
	// Handle "Ns" format (e.g. "60s", "300s", "10s")
	if len(step) > 1 && step[len(step)-1] == 's' {
		numStr := step[:len(step)-1]
		if num, err := strconv.Atoi(numStr); err == nil && num > 0 {
			return int64(num) * 1000
		}
	}
	return 60000
}
