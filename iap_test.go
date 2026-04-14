package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLooksLikeJWT(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid JWT", "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJhY2NvdW50cy5nb29nbGUuY29tIn0.signature", true},
		{"empty string", "", false},
		{"single part", "eyJhbGciOiJSUzI1NiJ9", false},
		{"two parts", "header.payload", false},
		{"four parts", "a.b.c.d", false},
		{"empty first part", ".payload.signature", false},
		{"empty middle part", "header..signature", false},
		{"empty last part", "header.payload.", false},
		{"access token", "ya29.a0ARrdaM_something", false},
		{"grafana token", "glsa_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx_xxxxxxxx", false},
		{"gcloud error on stdout", "ERROR: (gcloud.auth.print-identity-token) something went wrong", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeJWT(tt.input); got != tt.want {
				t.Errorf("looksLikeJWT(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIAPTransport_SetsProxyAuthorization(t *testing.T) {
	const fakeToken = "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ0ZXN0In0.sig"

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &iapTransport{
		iapToken: fakeToken,
		base:     http.DefaultTransport,
	}

	req, err := http.NewRequest("GET", srv.URL+"/api/datasources", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer grafana-token")
	req.Header.Set("Accept", "application/json")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Proxy-Authorization must be set with the IAP token.
	if got := gotHeaders.Get("Proxy-Authorization"); got != "Bearer "+fakeToken {
		t.Errorf("Proxy-Authorization = %q, want %q", got, "Bearer "+fakeToken)
	}
	// Authorization must be preserved for Grafana.
	if got := gotHeaders.Get("Authorization"); got != "Bearer grafana-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer grafana-token")
	}
	// Accept must be preserved.
	if got := gotHeaders.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want %q", got, "application/json")
	}
}

func TestIAPTransport_DoesNotMutateOriginalRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	transport := &iapTransport{
		iapToken: "eyJ0.eyJ0.sig",
		base:     http.DefaultTransport,
	}

	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Original request must NOT have Proxy-Authorization (it was cloned).
	if got := req.Header.Get("Proxy-Authorization"); got != "" {
		t.Errorf("original request has Proxy-Authorization = %q, want empty", got)
	}
}

func TestDualAuth_BothHeadersReachServer(t *testing.T) {
	const (
		iapToken     = "eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJ0ZXN0In0.sig"
		grafanaToken = "glsa_testtoken_abc123"
	)

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	gc := &GrafanaClient{
		baseURL: srv.URL,
		token:   grafanaToken,
		client: &http.Client{
			Transport: &iapTransport{iapToken: iapToken, base: http.DefaultTransport},
		},
	}

	// Call a real method that exercises the full request path.
	_, err := gc.ListDatasources()
	if err != nil {
		t.Fatal(err)
	}

	if got := gotHeaders.Get("Proxy-Authorization"); got != "Bearer "+iapToken {
		t.Errorf("Proxy-Authorization = %q, want %q", got, "Bearer "+iapToken)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer "+grafanaToken {
		t.Errorf("Authorization = %q, want %q", got, "Bearer "+grafanaToken)
	}
}

func TestHttpError_IAPRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Goog-Iap-Generated-Response", "true")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Invalid IAP credentials: Unable to parse JWT"))
	}))
	defer srv.Close()

	gc := &GrafanaClient{
		baseURL: srv.URL,
		token:   "glsa_test",
		client:  &http.Client{},
	}

	_, err := gc.ListDatasources()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "IAP authentication failed") {
		t.Errorf("expected IAP error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "GRAFANA_IAP_CLIENT_ID") {
		t.Errorf("expected hint about GRAFANA_IAP_CLIENT_ID, got: %v", err)
	}
}

func TestHttpError_GrafanaRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Invalid API key","statusCode":401}`))
	}))
	defer srv.Close()

	gc := &GrafanaClient{
		baseURL: srv.URL,
		token:   "glsa_bad",
		client:  &http.Client{},
	}

	_, err := gc.ListDatasources()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Grafana auth failed") {
		t.Errorf("expected Grafana auth error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "GRAFANA_TOKEN") {
		t.Errorf("expected hint about GRAFANA_TOKEN, got: %v", err)
	}
}

func TestHttpError_GenericError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	gc := &GrafanaClient{
		baseURL: srv.URL,
		token:   "glsa_test",
		client:  &http.Client{},
	}

	_, err := gc.ListDatasources()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected generic HTTP error, got: %v", err)
	}
	// Should NOT contain IAP or Grafana auth hints.
	if strings.Contains(err.Error(), "IAP") || strings.Contains(err.Error(), "GRAFANA_TOKEN") {
		t.Errorf("generic error should not have auth hints, got: %v", err)
	}
}
