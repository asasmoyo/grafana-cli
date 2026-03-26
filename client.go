package main

import (
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
	defaultLimit   = 100
	defaultStep    = "60s"
	requestTimeout = 30 * time.Second
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
	if token == "" {
		return nil, fmt.Errorf("GRAFANA_TOKEN environment variable is required (Service Account token)")
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
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 500))
	}
	return body, nil
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
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	return ds, nil
}

func (g *GrafanaClient) FindDatasource(nameOrID string, dsType string) (*Datasource, error) {
	datasources, err := g.ListDatasources()
	if err != nil {
		return nil, err
	}

	// Try by ID first
	if id, err := strconv.Atoi(nameOrID); err == nil {
		for _, ds := range datasources {
			if ds.ID == id {
				return &ds, nil
			}
		}
	}

	// Then by name (case-insensitive partial match)
	nameOrID = strings.ToLower(nameOrID)
	for _, ds := range datasources {
		if strings.ToLower(ds.Name) == nameOrID || strings.Contains(strings.ToLower(ds.Name), nameOrID) {
			if dsType == "" || strings.Contains(strings.ToLower(ds.Type), dsType) {
				return &ds, nil
			}
		}
	}

	// Then by type if nameOrID matches a type
	for _, ds := range datasources {
		if strings.Contains(strings.ToLower(ds.Type), nameOrID) {
			return &ds, nil
		}
	}

	return nil, fmt.Errorf("datasource %q not found (use 'datasources' to list available ones)", nameOrID)
}

func (g *GrafanaClient) proxyPath(dsID int, subpath string) string {
	return fmt.Sprintf("/api/datasources/proxy/%d/%s", dsID, subpath)
}
