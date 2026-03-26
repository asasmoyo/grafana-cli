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

// --- Tempo queries ---

func (g *GrafanaClient) TempoTrace(dsID int, traceID string) (string, error) {
	body, err := g.get(g.proxyPath(dsID, "api/traces/"+url.PathEscape(traceID)))
	if err != nil {
		return "", err
	}
	return formatTempoTrace(body)
}

func (g *GrafanaClient) TempoSearch(dsID int, query, start, end string, limit int) (string, error) {
	now := time.Now().UTC()
	if end == "" {
		end = fmt.Sprintf("%d", now.Unix())
	}
	if start == "" {
		start = fmt.Sprintf("%d", now.Add(-1*time.Hour).Unix())
	}

	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	params.Set("start", start)
	params.Set("end", end)
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	} else {
		params.Set("limit", strconv.Itoa(defaultLimit))
	}
	path := g.proxyPath(dsID, "api/search?"+params.Encode())
	body, err := g.get(path)
	if err != nil {
		return "", err
	}
	return formatTempoSearch(body)
}

// --- Tempo formatters ---

func formatTempoTrace(body []byte) (string, error) {
	var trace struct {
		Batches []struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						StringValue string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					TraceID           string          `json:"traceId"`
					SpanID            string          `json:"spanId"`
					ParentSpanID      string          `json:"parentSpanId"`
					Name              string          `json:"name"`
					Kind              json.RawMessage `json:"kind"`
					StartTimeUnixNano string          `json:"startTimeUnixNano"`
					EndTimeUnixNano   string          `json:"endTimeUnixNano"`
					Status            struct {
						Code    json.RawMessage `json:"code"`
						Message string          `json:"message"`
					} `json:"status"`
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue string `json:"stringValue"`
							IntValue    string `json:"intValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"batches"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		// Fallback: try to pretty-print whatever we got
		var generic interface{}
		if err2 := json.Unmarshal(body, &generic); err2 != nil {
			return "", fmt.Errorf("parsing tempo trace: %w", err)
		}
		out, _ := json.MarshalIndent(generic, "", "  ")
		return truncate(string(out), 5000), nil
	}

	var sb strings.Builder
	spanCount := 0
	for _, batch := range trace.Batches {
		// Extract service name
		svc := "unknown"
		for _, attr := range batch.Resource.Attributes {
			if attr.Key == "service.name" {
				svc = attr.Value.StringValue
				break
			}
		}
		for _, scope := range batch.ScopeSpans {
			for _, span := range scope.Spans {
				spanCount++
				startNano, _ := strconv.ParseInt(span.StartTimeUnixNano, 10, 64)
				endNano, _ := strconv.ParseInt(span.EndTimeUnixNano, 10, 64)
				dur := time.Duration(endNano - startNano)
				startTime := time.Unix(0, startNano).UTC().Format("15:04:05.000")

				// Parse status code (can be int or string like "STATUS_CODE_ERROR")
				isError := false
				statusStr := "OK"
				codeStr := strings.Trim(string(span.Status.Code), "\"")
				if codeStr == "2" || strings.Contains(codeStr, "ERROR") {
					isError = true
					statusStr = "ERROR"
				}

				// Parse kind (can be int or string like "SPAN_KIND_SERVER")
				kind := strings.Trim(string(span.Kind), "\"")
				kind = strings.TrimPrefix(kind, "SPAN_KIND_")
				if k, err := strconv.Atoi(kind); err == nil {
					switch k {
					case 1:
						kind = "INTERNAL"
					case 2:
						kind = "SERVER"
					case 3:
						kind = "CLIENT"
					case 4:
						kind = "PRODUCER"
					case 5:
						kind = "CONSUMER"
					default:
						kind = "UNSPECIFIED"
					}
				}

				indent := ""
				if span.ParentSpanID != "" {
					indent = "  "
				}

				spanName := span.Name
				if spanName == "" {
					// Try to derive name from attributes
					for _, attr := range span.Attributes {
						if attr.Key == "http.target" || attr.Key == "http.route" {
							spanName = attr.Value.StringValue
							break
						}
					}
					if spanName == "" {
						spanName = "(unnamed)"
					}
				}

				spanID := span.SpanID
				if len(spanID) > 12 {
					spanID = spanID[:12]
				}

				fmt.Fprintf(&sb, "%s[%s] %s %s (%s) [%s/%s] span=%s\n",
					indent, startTime, svc, spanName, dur, statusStr, kind, spanID)

				// Show key attributes for error spans
				if isError {
					for _, attr := range span.Attributes {
						if strings.Contains(attr.Key, "error") || strings.Contains(attr.Key, "exception") ||
							attr.Key == "http.status_code" {
							val := attr.Value.StringValue
							if val == "" {
								val = attr.Value.IntValue
							}
							fmt.Fprintf(&sb, "%s    %s=%s\n", indent, attr.Key, val)
						}
					}
				}
			}
		}
	}

	if spanCount == 0 {
		return "(no spans found in trace)", nil
	}
	fmt.Fprintf(&sb, "\n--- %d spans ---\n", spanCount)
	return sb.String(), nil
}

func formatTempoSearch(body []byte) (string, error) {
	var resp struct {
		Traces []struct {
			TraceID           string `json:"traceID"`
			RootServiceName   string `json:"rootServiceName"`
			RootTraceName     string `json:"rootTraceName"`
			StartTimeUnixNano string `json:"startTimeUnixNano"`
			DurationMs        int    `json:"durationMs"`
			SpanSets          []struct {
				Matched int `json:"matched"`
			} `json:"spanSets"`
		} `json:"traces"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parsing tempo search: %w", err)
	}

	if len(resp.Traces) == 0 {
		return "(no traces found)", nil
	}

	var sb strings.Builder
	w := tabwriter.NewWriter(&sb, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "TRACE_ID\tSERVICE\tROOT_SPAN\tDURATION\tSTART_TIME\n")
	for _, t := range resp.Traces {
		startNano, _ := strconv.ParseInt(t.StartTimeUnixNano, 10, 64)
		startTime := time.Unix(0, startNano).UTC().Format("2006-01-02 15:04:05")
		dur := fmt.Sprintf("%dms", t.DurationMs)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.TraceID, t.RootServiceName, t.RootTraceName, dur, startTime)
	}
	w.Flush()
	fmt.Fprintf(&sb, "\n--- %d traces ---\n", len(resp.Traces))
	return sb.String(), nil
}
