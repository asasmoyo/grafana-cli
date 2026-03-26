package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// --- Loki queries ---

func (g *GrafanaClient) LokiQuery(dsID int, query, start, end string, limit int, direction, format string) (string, error) {
	now := time.Now().UTC()
	if end == "" {
		end = fmt.Sprintf("%d", now.UnixNano())
	}
	if start == "" {
		start = fmt.Sprintf("%d", now.Add(-1*time.Hour).UnixNano())
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if direction == "" {
		direction = "backward"
	}

	params := url.Values{
		"query":     {query},
		"start":     {start},
		"end":       {end},
		"limit":     {strconv.Itoa(limit)},
		"direction": {direction},
	}
	path := g.proxyPath(dsID, "loki/api/v1/query_range?"+params.Encode())
	body, err := g.get(path)
	if err != nil {
		return "", err
	}
	return formatLokiResponse(body, format)
}

func (g *GrafanaClient) LokiCount(dsID int, query, start, end, step, format string) (string, error) {
	now := time.Now().UTC()
	if end == "" {
		end = fmt.Sprintf("%d", now.UnixNano())
	}
	if start == "" {
		start = fmt.Sprintf("%d", now.Add(-1*time.Hour).UnixNano())
	}
	if step == "" {
		step = "1m"
	}

	metricQuery := fmt.Sprintf("count_over_time(%s[%s])", query, step)

	params := url.Values{
		"query": {metricQuery},
		"start": {start},
		"end":   {end},
		"step":  {step},
	}
	path := g.proxyPath(dsID, "loki/api/v1/query_range?"+params.Encode())
	body, err := g.get(path)
	if err != nil {
		return "", err
	}
	return formatLokiCountResponse(body, format)
}

func (g *GrafanaClient) LokiLabels(dsID int) (string, error) {
	body, err := g.get(g.proxyPath(dsID, "loki/api/v1/labels"))
	if err != nil {
		return "", err
	}
	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing loki labels: %w", err)
	}
	return strings.Join(resp.Data, "\n"), nil
}

func (g *GrafanaClient) LokiLabelValues(dsID int, label string) (string, error) {
	body, err := g.get(g.proxyPath(dsID, "loki/api/v1/label/"+url.PathEscape(label)+"/values"))
	if err != nil {
		return "", err
	}
	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing loki label-values: %w", err)
	}
	return strings.Join(resp.Data, "\n"), nil
}

// --- Loki formatters ---

func formatLokiResponse(body []byte, format string) (string, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
			Stats struct {
				Summary struct {
					BytesProcessedPerSecond int `json:"bytesProcessedPerSecond"`
					TotalLinesProcessed     int `json:"totalLinesProcessed"`
				} `json:"summary"`
			} `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing loki response: %w", err)
	}

	var sb strings.Builder
	totalLines := 0

	if format == "tsv" {
		for _, stream := range resp.Data.Result {
			for _, entry := range stream.Values {
				if len(entry) < 2 {
					continue
				}
				totalLines++
				ts, _ := strconv.ParseInt(entry[0], 10, 64)
				t := time.Unix(0, ts).UTC().Format("2006-01-02T15:04:05Z")
				fmt.Fprintf(&sb, "%s\t%s\n", t, entry[1])
			}
		}
		if totalLines == 0 {
			return "(no log lines found)", nil
		}
		return sb.String(), nil
	}

	// Build compact label representation for each stream
	// showing only the most useful labels for log identification
	lokiKeyLabels := []string{"namespace", "pod", "container", "service_name", "component", "job", "stream"}

	for _, stream := range resp.Data.Result {
		// Show only key labels for compact log output
		var labelParts []string
		for _, k := range lokiKeyLabels {
			if v, ok := stream.Stream[k]; ok {
				labelParts = append(labelParts, k+"="+v)
			}
		}
		labels := strings.Join(labelParts, ", ")

		for _, entry := range stream.Values {
			if len(entry) < 2 {
				continue
			}
			totalLines++
			ts, _ := strconv.ParseInt(entry[0], 10, 64)
			t := time.Unix(0, ts).UTC().Format("2006-01-02 15:04:05")
			// Truncate very long log lines
			line := entry[1]
			if len(line) > 500 {
				line = line[:500] + "... (truncated)"
			}
			fmt.Fprintf(&sb, "[%s] {%s} %s\n", t, labels, line)
		}
	}

	if totalLines == 0 {
		return "(no log lines found)", nil
	}
	fmt.Fprintf(&sb, "\n--- %d log lines returned ---\n", totalLines)
	return sb.String(), nil
}

func formatLokiCountResponse(body []byte, format string) (string, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][]interface{}   `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing loki count response: %w", err)
	}

	if len(resp.Data.Result) == 0 {
		return "(no data)", nil
	}

	var sb strings.Builder
	totalCount := 0.0
	totalBuckets := 0

	for i, series := range resp.Data.Result {
		if format != "tsv" {
			if i > 0 {
				sb.WriteString("\n")
			}
			if len(series.Metric) > 0 {
				labels := formatLabels(series.Metric)
				fmt.Fprintf(&sb, "── %s ──\n", labels)
			}
			w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "TIME\tCOUNT\n")
			for _, v := range series.Values {
				if len(v) < 2 {
					continue
				}
				tsRaw, ok := v[0].(float64)
				if !ok {
					continue
				}
				totalBuckets++
				t := time.Unix(int64(tsRaw), 0).UTC().Format("2006-01-02 15:04:05")
				valStr := fmt.Sprintf("%v", v[1])
				count, _ := strconv.ParseFloat(valStr, 64)
				totalCount += count
				fmt.Fprintf(w, "%s\t%s\n", t, valStr)
			}
			w.Flush()
		} else {
			for _, v := range series.Values {
				if len(v) < 2 {
					continue
				}
				tsRaw, ok := v[0].(float64)
				if !ok {
					continue
				}
				totalBuckets++
				t := time.Unix(int64(tsRaw), 0).UTC().Format("2006-01-02T15:04:05Z")
				valStr := fmt.Sprintf("%v", v[1])
				count, _ := strconv.ParseFloat(valStr, 64)
				totalCount += count
				fmt.Fprintf(&sb, "%s\t%s\n", t, valStr)
			}
		}
	}

	if format != "tsv" {
		fmt.Fprintf(&sb, "\n--- total: %.0f log lines across %d buckets ---\n", totalCount, totalBuckets)
	}
	return sb.String(), nil
}
