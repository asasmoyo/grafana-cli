package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultLimit    = 100
	defaultStep     = "60s"
	requestTimeout  = 30 * time.Second
	maxResponseBody = 50 * 1024 * 1024 // 50MB
)

// --- Grafana client ---

type GrafanaClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewGrafanaClient() (*GrafanaClient, error) {
	baseURL := os.Getenv("GRAFANA_URL")
	token := os.Getenv("GRAFANA_TOKEN")
	if baseURL == "" {
		return nil, fmt.Errorf("GRAFANA_URL environment variable is required (e.g. https://grafana.example.com)")
	}

	// Optional IAP authentication
	iapClientID := os.Getenv("GRAFANA_IAP_CLIENT_ID")
	iapSA := os.Getenv("GRAFANA_IAP_SA")
	switch {
	case iapClientID != "" && iapSA == "":
		return nil, fmt.Errorf("both GRAFANA_IAP_CLIENT_ID and GRAFANA_IAP_SA must be set (got only GRAFANA_IAP_CLIENT_ID)")
	case iapClientID == "" && iapSA != "":
		return nil, fmt.Errorf("both GRAFANA_IAP_CLIENT_ID and GRAFANA_IAP_SA must be set (got only GRAFANA_IAP_SA)")
	}

	if token == "" {
		return nil, fmt.Errorf("GRAFANA_TOKEN environment variable is required (Service Account token)")
	}

	httpClient := &http.Client{Timeout: requestTimeout}
	if iapClientID != "" {
		iapToken, err := getIAPToken(context.Background(), iapClientID, iapSA)
		if err != nil {
			return nil, fmt.Errorf("obtaining IAP token: %w", err)
		}
		httpClient.Transport = &iapTransport{iapToken: iapToken, base: http.DefaultTransport}
	}

	return &GrafanaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  httpClient,
	}, nil
}

func (g *GrafanaClient) get(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", g.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if int64(len(body)) == maxResponseBody {
		fmt.Fprintf(os.Stderr, "warning: response truncated at %dMB, results may be incomplete\n", maxResponseBody/1024/1024)
	}
	if resp.StatusCode != 200 {
		return nil, httpError(resp, body)
	}
	return body, nil
}

func httpError(resp *http.Response, body []byte) error {
	msg := truncate(string(body), 500)
	if resp.Header.Get("X-Goog-Iap-Generated-Response") == "true" {
		return fmt.Errorf("IAP authentication failed (HTTP %d): %s\n  Check GRAFANA_IAP_CLIENT_ID and GRAFANA_IAP_SA, and verify the service account has roles/iap.httpsResourceAccessor", resp.StatusCode, msg)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("Grafana auth failed (HTTP %d): %s\n  Check GRAFANA_TOKEN is valid for %s", resp.StatusCode, msg, resp.Request.URL.Host)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
}

// --- Datasource discovery ---

type Datasource struct {
	ID        int    `json:"id"`
	UID       string `json:"uid"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	URL       string `json:"url"`
	IsDefault bool   `json:"isDefault"`
}

func (g *GrafanaClient) ListDatasources() ([]Datasource, error) {
	body, err := g.get("/api/datasources")
	if err != nil {
		return nil, err
	}
	var ds []Datasource
	if err := json.Unmarshal(body, &ds); err != nil {
		return nil, fmt.Errorf("parsing datasources: %w", err)
	}
	// Sorted by name rather than id: the numeric id is the legacy Grafana <13
	// identity and may be absent, which would make the listing order arbitrary.
	sort.Slice(ds, func(i, j int) bool {
		ni, nj := strings.ToLower(ds[i].Name), strings.ToLower(ds[j].Name)
		if ni != nj {
			return ni < nj
		}
		return ds[i].UID < ds[j].UID
	})
	return ds, nil
}

// typeMatches reports whether ds satisfies the datasource type a command
// requires ("prometheus", "loki", "tempo", "stackdriver"). An empty constraint
// matches every datasource.
func typeMatches(ds Datasource, dsType string) bool {
	if dsType == "" {
		return true
	}
	return strings.Contains(strings.ToLower(ds.Type), strings.ToLower(dsType))
}

// FindDatasource resolves a datasource selector supplied on the command line.
//
// Selectors are matched in a fixed order so that the result is deterministic:
//
//  1. uid        exact, case-insensitive  — the Grafana 13 native identity
//  2. numeric id exact                    — legacy identity, Grafana < 13 only
//  3. name       exact, case-insensitive
//  4. type       exact, case-insensitive
//  5. name       substring, case-insensitive
//  6. type       substring, case-insensitive
//
// dsType constrains the result to datasources of that type, at every stage. A
// selector that unambiguously identifies a datasource of the wrong type is
// reported as such instead of "not found", and an ambiguous selector is an
// error listing the candidates instead of a silent arbitrary pick.
func (g *GrafanaClient) FindDatasource(selector string, dsType string) (*Datasource, error) {
	datasources, err := g.ListDatasources()
	if err != nil {
		return nil, err
	}
	return findDatasource(datasources, selector, dsType)
}

func findDatasource(all []Datasource, selector, dsType string) (*Datasource, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, fmt.Errorf("empty datasource argument%s", availableHint(all, dsType))
	}
	sel := strings.ToLower(selector)
	id, idErr := strconv.Atoi(selector)

	stages := []struct {
		kind  string
		match func(Datasource) bool
	}{
		{"uid", func(ds Datasource) bool { return strings.EqualFold(ds.UID, selector) }},
		{"id", func(ds Datasource) bool { return idErr == nil && ds.ID != 0 && ds.ID == id }},
		{"name", func(ds Datasource) bool { return strings.ToLower(ds.Name) == sel }},
		{"type", func(ds Datasource) bool { return strings.ToLower(ds.Type) == sel }},
		{"name", func(ds Datasource) bool { return strings.Contains(strings.ToLower(ds.Name), sel) }},
		{"type", func(ds Datasource) bool { return strings.Contains(strings.ToLower(ds.Type), sel) }},
	}

	for _, stage := range stages {
		var matched, typed []Datasource
		for _, ds := range all {
			if !stage.match(ds) {
				continue
			}
			matched = append(matched, ds)
			if typeMatches(ds, dsType) {
				typed = append(typed, ds)
			}
		}
		switch {
		case len(matched) == 0:
			continue
		case len(typed) == 1:
			ds := typed[0]
			return &ds, nil
		case len(typed) == 0:
			// The selector names real datasources, all of the wrong type. Only
			// worth reporting when it is unambiguous; otherwise keep looking.
			if len(matched) == 1 {
				return nil, fmt.Errorf("datasource %q (%s) is of type %q, but this command requires a %q datasource%s",
					matched[0].Name, matched[0].UID, matched[0].Type, dsType, availableHint(all, dsType))
			}
			continue
		default:
			// Several equally good candidates. Prefer the default datasource if
			// exactly one of them is marked as such, otherwise refuse to guess.
			var defaults []Datasource
			for _, ds := range typed {
				if ds.IsDefault {
					defaults = append(defaults, ds)
				}
			}
			if len(defaults) == 1 {
				fmt.Fprintf(os.Stderr, "note: %q matches %d datasources by %s; using the default one (%s, uid=%s)\n",
					selector, len(typed), stage.kind, defaults[0].Name, defaults[0].UID)
				ds := defaults[0]
				return &ds, nil
			}
			return nil, fmt.Errorf("datasource %q is ambiguous — it matches %d datasources by %s:\n%s\n  pass a uid to select exactly one",
				selector, len(typed), stage.kind, datasourceList(typed))
		}
	}

	return nil, fmt.Errorf("datasource %q not found%s", selector, availableHint(all, dsType))
}

// maxHintedDatasources caps how many datasources are echoed back in an error.
const maxHintedDatasources = 25

func datasourceList(ds []Datasource) string {
	var sb strings.Builder
	for i, d := range ds {
		if i == maxHintedDatasources {
			fmt.Fprintf(&sb, "    ... and %d more (run 'grafana-cli datasources')", len(ds)-i)
			break
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "    %-22s %s (%s)", d.UID, d.Name, d.Type)
	}
	return sb.String()
}

// availableHint lists the datasources a failed selector could have matched, so
// that a caller can retry without a second round trip.
func availableHint(all []Datasource, dsType string) string {
	var candidates []Datasource
	for _, ds := range all {
		if typeMatches(ds, dsType) {
			candidates = append(candidates, ds)
		}
	}
	if len(candidates) == 0 {
		if dsType != "" {
			return fmt.Sprintf("\n  no %s datasources are configured (run 'grafana-cli datasources' to list all types)", dsType)
		}
		return "\n  no datasources are configured"
	}
	label := "available datasources"
	if dsType != "" {
		label = fmt.Sprintf("available %s datasources", dsType)
	}
	return fmt.Sprintf("\n  %s:\n%s", label, datasourceList(candidates))
}

func (g *GrafanaClient) proxyPath(dsUID string, subpath string) string {
	return fmt.Sprintf("/api/datasources/proxy/uid/%s/%s", dsUID, subpath)
}

func (g *GrafanaClient) post(path string, body []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", g.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if int64(len(respBody)) == maxResponseBody {
		fmt.Fprintf(os.Stderr, "warning: response truncated at %dMB, results may be incomplete\n", maxResponseBody/1024/1024)
	}
	if resp.StatusCode != 200 {
		return nil, httpError(resp, respBody)
	}
	return respBody, nil
}

func (g *GrafanaClient) resourceGet(dsUID, subpath string) ([]byte, error) {
	return g.get(fmt.Sprintf("/api/datasources/uid/%s/resources/%s", dsUID, subpath))
}
