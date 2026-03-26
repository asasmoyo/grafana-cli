package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"
)

// --- Prometheus queries ---

func (g *GrafanaClient) PromQueryRange(dsID int, query, start, end, step, format string) (string, error) {
	now := time.Now().UTC()
	if end == "" {
		end = fmt.Sprintf("%d", now.Unix())
	}
	if start == "" {
		start = fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix())
	}
	if step == "" {
		step = defaultStep
	}

	params := url.Values{
		"query": {query},
		"start": {start},
		"end":   {end},
		"step":  {step},
	}
	path := g.proxyPath(dsID, "api/v1/query_range?"+params.Encode())
	body, err := g.get(path)
	if err != nil {
		return "", err
	}
	return formatPromResponse(body, format)
}

func (g *GrafanaClient) PromQueryInstant(dsID int, query, ts, format string) (string, error) {
	params := url.Values{"query": {query}}
	if ts != "" {
		params.Set("time", ts)
	}
	path := g.proxyPath(dsID, "api/v1/query?"+params.Encode())
	body, err := g.get(path)
	if err != nil {
		return "", err
	}
	return formatPromResponse(body, format)
}

func (g *GrafanaClient) PromLabels(dsID int) (string, error) {
	body, err := g.get(g.proxyPath(dsID, "api/v1/labels"))
	if err != nil {
		return "", err
	}
	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing prom labels: %w", err)
	}
	return strings.Join(resp.Data, "\n"), nil
}

func (g *GrafanaClient) PromLabelValues(dsID int, label string) (string, error) {
	body, err := g.get(g.proxyPath(dsID, "api/v1/label/"+url.PathEscape(label)+"/values"))
	if err != nil {
		return "", err
	}
	var resp struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing prom label-values: %w", err)
	}
	return strings.Join(resp.Data, "\n"), nil
}

func (g *GrafanaClient) PromSeries(dsID int, match string) (string, error) {
	params := url.Values{"match[]": {match}}
	body, err := g.get(g.proxyPath(dsID, "api/v1/series?"+params.Encode()))
	if err != nil {
		return "", err
	}
	var resp struct {
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing prom series: %w", err)
	}
	var sb strings.Builder
	for i, s := range resp.Data {
		if i >= defaultLimit {
			fmt.Fprintf(&sb, "... (%d more series truncated)\n", len(resp.Data)-defaultLimit)
			break
		}
		sb.WriteString(formatLabels(s))
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// --- Prometheus formatter ---

func formatPromResponse(body []byte, format string) (string, error) {
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return "", fmt.Errorf("prometheus query failed: status=%s", resp.Status)
	}

	var sb strings.Builder
	switch resp.Data.ResultType {
	case "vector":
		var results []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		}
		if err := json.Unmarshal(resp.Data.Result, &results); err != nil {
			return "", fmt.Errorf("parsing vector result: %w", err)
		}
		if len(results) == 0 {
			return "(no results)", nil
		}
		if format == "tsv" {
			for _, r := range results {
				if len(r.Value) < 2 {
					continue
				}
				labels := formatLabels(r.Metric)
				val := fmt.Sprintf("%v", r.Value[1])
				fmt.Fprintf(&sb, "%s\t%s\n", labels, val)
			}
		} else {
			w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "METRIC\tVALUE\n")
			for i, r := range results {
				if i >= 200 {
					fmt.Fprintf(w, "... (%d more results truncated)\n", len(results)-200)
					break
				}
				if len(r.Value) < 2 {
					continue
				}
				labels := formatLabels(r.Metric)
				val := fmt.Sprintf("%v", r.Value[1])
				fmt.Fprintf(w, "%s\t%s\n", labels, val)
			}
			w.Flush()
		}

	case "matrix":
		var results []struct {
			Metric map[string]string `json:"metric"`
			Values [][]interface{}   `json:"values"`
		}
		if err := json.Unmarshal(resp.Data.Result, &results); err != nil {
			return "", fmt.Errorf("parsing matrix result: %w", err)
		}
		if len(results) == 0 {
			return "(no results)", nil
		}
		if format == "tsv" {
			for _, r := range results {
				if len(results) > 1 {
					fmt.Fprintf(&sb, "# %s\n", formatLabels(r.Metric))
				}
				for _, v := range r.Values {
					if len(v) < 2 {
						continue
					}
					tsRaw, ok := v[0].(float64)
					if !ok {
						continue
					}
					t := time.Unix(int64(tsRaw), 0).UTC().Format("2006-01-02T15:04:05Z")
					val := fmt.Sprintf("%v", v[1])
					fmt.Fprintf(&sb, "%s\t%s\n", t, val)
				}
			}
		} else {
			for i, r := range results {
				if i >= 50 {
					fmt.Fprintf(&sb, "\n... (%d more series truncated)\n", len(results)-50)
					break
				}
				labels := formatLabels(r.Metric)
				fmt.Fprintf(&sb, "── %s (%d samples) ──\n", labels, len(r.Values))
				w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
				fmt.Fprintf(w, "TIME\tVALUE\n")
				// Show at most 50 samples, evenly distributed
				step := 1
				if len(r.Values) > 50 {
					step = len(r.Values) / 50
				}
				for j := 0; j < len(r.Values); j += step {
					if len(r.Values[j]) < 2 {
						continue
					}
					tsRaw, ok := r.Values[j][0].(float64)
					if !ok {
						continue
					}
					t := time.Unix(int64(tsRaw), 0).UTC().Format("2006-01-02 15:04:05")
					val := fmt.Sprintf("%v", r.Values[j][1])
					fmt.Fprintf(w, "%s\t%s\n", t, val)
				}
				w.Flush()
				sb.WriteString("\n")
			}
		}

	case "scalar":
		var val []interface{}
		if err := json.Unmarshal(resp.Data.Result, &val); err != nil {
			return "", fmt.Errorf("parsing scalar result: %w", err)
		}
		if len(val) >= 2 {
			fmt.Fprintf(&sb, "%v", val[1])
		} else {
			return "(no results)", nil
		}

	default:
		// Fallback: compact JSON
		sb.WriteString(string(resp.Data.Result))
	}

	return sb.String(), nil
}
