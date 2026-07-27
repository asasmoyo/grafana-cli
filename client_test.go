package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// A fake Grafana that behaves like a Grafana 13 instance: the legacy numeric-id
// datasource routes are gone, so any request to /api/datasources/proxy/<id>/...
// answers 404, exactly as a real server does after the upgrade. Every request
// is recorded so that tests can assert the exact URL the client built.
//
// The fixture below is arbitrary: it exists to exercise resolution (several
// datasources, two of the same type, one default) and is not modelled on any
// particular deployment.

const fakeDatasourcesJSON = `[
	{"id":864,"uid":"PROMUID","name":"Prometheus","type":"prometheus","isDefault":true},
	{"id":12,"uid":"LOKIUID","name":"Loki","type":"loki"},
	{"id":13,"uid":"LOKI2UID","name":"Loki long-term","type":"loki"},
	{"id":7,"uid":"TEMPOUID","name":"Tempo","type":"tempo"},
	{"id":99,"uid":"GCMUID","name":"Google Cloud Monitoring","type":"stackdriver"}
]`

type recordedRequest struct {
	Method string
	Path   string // decoded path
	Raw    string // path as it appeared on the wire, still percent-encoded
	Query  url.Values
	Body   string
	Header http.Header
}

type fakeGrafana struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
}

func (f *fakeGrafana) record(r *http.Request, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Raw:    r.URL.EscapedPath(),
		Query:  r.URL.Query(),
		Body:   body,
		Header: r.Header.Clone(),
	})
}

func (f *fakeGrafana) last(t *testing.T) recordedRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request was recorded")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeGrafana) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.requests))
	for i, r := range f.requests {
		out[i] = r.Path
	}
	return out
}

// newFakeGrafanaServer starts a fake Grafana. Use newFakeGrafana when the test
// needs a client wired to it; the CLI tests drive it through the binary.
func newFakeGrafanaServer(t *testing.T) *fakeGrafana {
	t.Helper()
	f := &fakeGrafana{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody []byte
		if r.Body != nil {
			reqBody, _ = io.ReadAll(r.Body)
		}
		f.record(r, string(reqBody))

		body, ok := fakeResponse(r)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not found"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

// newFakeGrafana starts a fake Grafana and points a client at it.
func newFakeGrafana(t *testing.T) (*fakeGrafana, *GrafanaClient) {
	t.Helper()
	f := newFakeGrafanaServer(t)

	t.Setenv("GRAFANA_URL", f.URL)
	t.Setenv("GRAFANA_TOKEN", "test-token")
	t.Setenv("GRAFANA_IAP_CLIENT_ID", "")
	t.Setenv("GRAFANA_IAP_SA", "")

	gc, err := NewGrafanaClient()
	if err != nil {
		t.Fatalf("NewGrafanaClient: %v", err)
	}
	return f, gc
}

// knownUID reports whether uid identifies one of the fixture datasources. Real
// Grafana 404s on anything else, which is what catches a caller that passes a
// numeric id, a name, or an unresolved selector where a uid is expected.
func knownUID(uid string) bool {
	for _, ds := range []string{"PROMUID", "LOKIUID", "LOKI2UID", "TEMPOUID", "GCMUID"} {
		if uid == ds {
			return true
		}
	}
	return false
}

// fakeResponse maps a request to a canned payload. Only uid-based routes are
// served; everything else 404s like Grafana 13 does.
func fakeResponse(r *http.Request) (string, bool) {
	p := r.URL.Path

	switch {
	case p == "/api/datasources":
		return fakeDatasourcesJSON, true
	case p == "/api/ds/query":
		return `{"results":{"A":{"status":200,"frames":[{"schema":{"refId":"A","fields":[
			{"name":"Time","type":"time"},
			{"name":"Value","type":"number","labels":{"zone":"us-central1-a"}}]},
			"data":{"values":[[1774452000000],[0.42]]}}]}}}`, true
	case strings.HasPrefix(p, "/api/datasources/uid/"):
		rest, _ := strings.CutPrefix(p, "/api/datasources/uid/")
		uid, sub, _ := strings.Cut(rest, "/")
		if knownUID(uid) && sub == "resources/projects" {
			return `[{"value":"my-project","label":"My Project"}]`, true
		}
		return "", false
	}

	rest, ok := strings.CutPrefix(p, "/api/datasources/proxy/uid/")
	if !ok {
		return "", false // legacy numeric-id proxy route, or something unknown
	}
	uid, sub, ok := strings.Cut(rest, "/")
	if !ok || !knownUID(uid) {
		return "", false
	}

	switch {
	case sub == "api/v1/query", sub == "api/v1/query_range":
		return `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"__name__":"up","job":"api"},"value":[1774452000,"1"]}]}}`, true
	case sub == "api/v1/labels":
		return `{"status":"success","data":["__name__","job","namespace"]}`, true
	case strings.HasPrefix(sub, "api/v1/label/"):
		return `{"status":"success","data":["api","web"]}`, true
	case sub == "api/v1/series":
		return `{"status":"success","data":[{"__name__":"up","job":"api"}]}`, true
	case sub == "loki/api/v1/query_range":
		return `{"status":"success","data":{"resultType":"streams","result":[
			{"stream":{"namespace":"default","pod":"api-0"},"values":[["1774452000000000000","boom"]]}]}}`, true
	case sub == "loki/api/v1/labels":
		return `{"status":"success","data":["namespace","pod"]}`, true
	case strings.HasPrefix(sub, "loki/api/v1/label/"):
		return `{"status":"success","data":["default","kube-system"]}`, true
	case strings.HasPrefix(sub, "api/traces/"):
		return `{"batches":[]}`, true
	case sub == "api/search":
		return `{"traces":[]}`, true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Route construction — the regression net for the Grafana 13 upgrade
// ---------------------------------------------------------------------------

func TestDatasourceRoutesAreUIDBased(t *testing.T) {
	f, gc := newFakeGrafana(t)

	tests := []struct {
		name  string
		call  func() (string, error)
		path  string
		query map[string]string
	}{
		{
			name:  "prom instant query",
			call:  func() (string, error) { return gc.PromQueryInstant("PROMUID", "up", "1774452000", "") },
			path:  "/api/datasources/proxy/uid/PROMUID/api/v1/query",
			query: map[string]string{"query": "up", "time": "1774452000"},
		},
		{
			name: "prom range query",
			call: func() (string, error) {
				return gc.PromQueryRange("PROMUID", "up", "1774448400", "1774452000", "60s", "")
			},
			path: "/api/datasources/proxy/uid/PROMUID/api/v1/query_range",
			query: map[string]string{
				"query": "up", "start": "1774448400", "end": "1774452000", "step": "60s",
			},
		},
		{
			name: "prom labels",
			call: func() (string, error) { return gc.PromLabels("PROMUID") },
			path: "/api/datasources/proxy/uid/PROMUID/api/v1/labels",
		},
		{
			name: "prom label values",
			call: func() (string, error) { return gc.PromLabelValues("PROMUID", "job") },
			path: "/api/datasources/proxy/uid/PROMUID/api/v1/label/job/values",
		},
		{
			name:  "prom series",
			call:  func() (string, error) { return gc.PromSeries("PROMUID", `up{job="api"}`) },
			path:  "/api/datasources/proxy/uid/PROMUID/api/v1/series",
			query: map[string]string{"match[]": `up{job="api"}`},
		},
		{
			name: "loki query",
			call: func() (string, error) {
				return gc.LokiQuery("LOKIUID", `{app="api"}`, "1774448400000000000", "1774452000000000000", 50, "forward", "")
			},
			path: "/api/datasources/proxy/uid/LOKIUID/loki/api/v1/query_range",
			query: map[string]string{
				"query": `{app="api"}`, "start": "1774448400000000000",
				"end": "1774452000000000000", "limit": "50", "direction": "forward",
			},
		},
		{
			name: "loki count",
			call: func() (string, error) {
				return gc.LokiCount("LOKIUID", `{app="api"}`, "1774448400000000000", "1774452000000000000", "5m", "")
			},
			path:  "/api/datasources/proxy/uid/LOKIUID/loki/api/v1/query_range",
			query: map[string]string{"query": `count_over_time({app="api"}[5m])`, "step": "5m"},
		},
		{
			name: "loki labels",
			call: func() (string, error) { return gc.LokiLabels("LOKIUID") },
			path: "/api/datasources/proxy/uid/LOKIUID/loki/api/v1/labels",
		},
		{
			name: "loki label values",
			call: func() (string, error) { return gc.LokiLabelValues("LOKIUID", "namespace") },
			path: "/api/datasources/proxy/uid/LOKIUID/loki/api/v1/label/namespace/values",
		},
		{
			name: "tempo trace",
			call: func() (string, error) { return gc.TempoTrace("TEMPOUID", "abc123") },
			path: "/api/datasources/proxy/uid/TEMPOUID/api/traces/abc123",
		},
		{
			name: "tempo search",
			call: func() (string, error) {
				return gc.TempoSearch("TEMPOUID", "{ duration > 1s }", "1774448400", "1774452000", 3)
			},
			path: "/api/datasources/proxy/uid/TEMPOUID/api/search",
			query: map[string]string{
				"q": "{ duration > 1s }", "start": "1774448400", "end": "1774452000", "limit": "3",
			},
		},
		{
			name: "gcm projects",
			call: func() (string, error) { return gc.GCMProjects("GCMUID") },
			path: "/api/datasources/uid/GCMUID/resources/projects",
		},
		{
			name: "gcm query",
			call: func() (string, error) {
				return gc.GCMQuery("GCMUID", "my-project", "run_googleapis_com:request_count", "1774448400000", "1774452000000", "60s", "")
			},
			path: "/api/ds/query",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.call(); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			got := f.last(t)
			if got.Path != tt.path {
				t.Errorf("path = %q, want %q", got.Path, tt.path)
			}
			for k, want := range tt.query {
				if v := got.Query.Get(k); v != want {
					t.Errorf("query %s = %q, want %q", k, v, want)
				}
			}
			if got.Header.Get("Authorization") != "Bearer test-token" {
				t.Errorf("Authorization = %q, want Bearer test-token", got.Header.Get("Authorization"))
			}
		})
	}

	// The upgrade that broke every command: a numeric id anywhere in a proxy
	// path means the client is using the routes Grafana 13 disabled.
	legacy := regexp.MustCompile(`/api/datasources/(proxy/)?[0-9]+(/|$)`)
	for _, p := range f.paths() {
		if legacy.MatchString(p) {
			t.Errorf("request used a legacy numeric-id datasource route: %s", p)
		}
	}
}

func TestGCMQuerySendsDatasourceUID(t *testing.T) {
	f, gc := newFakeGrafana(t)

	if _, err := gc.GCMQuery("GCMUID", "my-project", "up", "1774448400000", "1774452000000", "60s", ""); err != nil {
		t.Fatalf("GCMQuery: %v", err)
	}

	var payload struct {
		Queries []struct {
			Datasource  map[string]string `json:"datasource"`
			PromQLQuery struct {
				ProjectName string `json:"projectName"`
				Expr        string `json:"expr"`
				Step        string `json:"step"`
			} `json:"promQLQuery"`
		} `json:"queries"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal([]byte(f.last(t).Body), &payload); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if len(payload.Queries) != 1 {
		t.Fatalf("got %d queries, want 1", len(payload.Queries))
	}
	q := payload.Queries[0]
	if q.Datasource["uid"] != "GCMUID" {
		t.Errorf("datasource uid = %q, want GCMUID", q.Datasource["uid"])
	}
	if q.PromQLQuery.ProjectName != "my-project" || q.PromQLQuery.Expr != "up" {
		t.Errorf("unexpected promQLQuery: %+v", q.PromQLQuery)
	}
	if payload.From != "1774448400000" || payload.To != "1774452000000" {
		t.Errorf("from/to = %q/%q, want millisecond epochs", payload.From, payload.To)
	}
}

func TestProxyPathEscapesLabelNames(t *testing.T) {
	f, gc := newFakeGrafana(t)

	// A label containing a slash must not create extra path segments.
	if _, err := gc.PromLabelValues("PROMUID", "weird/label"); err != nil {
		t.Fatalf("PromLabelValues: %v", err)
	}
	want := "/api/datasources/proxy/uid/PROMUID/api/v1/label/weird%2Flabel/values"
	if got := f.last(t).Raw; got != want {
		t.Errorf("escaped path = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Datasource resolution
// ---------------------------------------------------------------------------

func fakeDatasources(t *testing.T) []Datasource {
	t.Helper()
	var ds []Datasource
	if err := json.Unmarshal([]byte(fakeDatasourcesJSON), &ds); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	return ds
}

func TestFindDatasource(t *testing.T) {
	all := fakeDatasources(t)

	tests := []struct {
		name     string
		selector string
		dsType   string
		wantUID  string
		errHas   string
	}{
		// Grafana 13 identity: the uid the `datasources` command prints must be
		// accepted as a selector. This used to fail with "not found".
		{name: "uid", selector: "PROMUID", dsType: "prometheus", wantUID: "PROMUID"},
		{name: "uid is case-insensitive", selector: "promuid", dsType: "prometheus", wantUID: "PROMUID"},
		{name: "uid without a type constraint", selector: "TEMPOUID", wantUID: "TEMPOUID"},

		// Legacy identity, still accepted for Grafana 12.
		{name: "numeric id", selector: "864", dsType: "prometheus", wantUID: "PROMUID"},
		{name: "numeric id without a type constraint", selector: "12", wantUID: "LOKIUID"},

		{name: "exact name", selector: "Prometheus", dsType: "prometheus", wantUID: "PROMUID"},
		{name: "exact name is case-insensitive", selector: "prometheus", dsType: "prometheus", wantUID: "PROMUID"},
		{name: "exact name wins over partial", selector: "Loki", dsType: "loki", wantUID: "LOKIUID"},
		{name: "exact type", selector: "tempo", dsType: "tempo", wantUID: "TEMPOUID"},
		{name: "exact type with spaces trimmed", selector: "  tempo  ", dsType: "tempo", wantUID: "TEMPOUID"},
		{name: "partial name", selector: "long-term", dsType: "loki", wantUID: "LOKI2UID"},
		{name: "partial name with spaces", selector: "google cloud", dsType: "stackdriver", wantUID: "GCMUID"},
		{name: "partial type", selector: "stackd", wantUID: "GCMUID"},

		// The bug that made `loki query 864 ...` proxy LogQL to Prometheus.
		{
			name:     "numeric id of the wrong type is rejected",
			selector: "864", dsType: "loki",
			errHas: `is of type "prometheus", but this command requires a "loki" datasource`,
		},
		{
			name:     "uid of the wrong type is rejected",
			selector: "PROMUID", dsType: "tempo",
			errHas: "requires a \"tempo\" datasource",
		},
		{
			name:     "name of the wrong type is rejected",
			selector: "Tempo", dsType: "prometheus",
			errHas: "requires a \"prometheus\" datasource",
		},
		{
			name:     "wrong type error lists the datasources that would work",
			selector: "Tempo", dsType: "loki",
			errHas: "LOKI2UID",
		},

		// Ambiguity is reported instead of silently resolved.
		{
			name: "ambiguous partial name", selector: "lok", dsType: "loki",
			errHas: "is ambiguous",
		},
		{
			name: "ambiguous partial name lists candidates", selector: "lok", dsType: "loki",
			errHas: "LOKI2UID",
		},

		{name: "unknown selector", selector: "nope", errHas: `datasource "nope" not found`},
		{name: "unknown selector lists candidates", selector: "nope", dsType: "loki", errHas: "available loki datasources"},
		{name: "empty selector", selector: "", errHas: "empty datasource argument"},
		{name: "unknown numeric id", selector: "4242", errHas: "not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds, err := findDatasource(all, tt.selector, tt.dsType)
			if tt.errHas != "" {
				requireErrContains(t, err, tt.errHas)
				return
			}
			if err != nil {
				t.Fatalf("findDatasource(%q, %q): %v", tt.selector, tt.dsType, err)
			}
			if ds.UID != tt.wantUID {
				t.Errorf("uid = %q, want %q", ds.UID, tt.wantUID)
			}
		})
	}
}

func TestFindDatasourceAmbiguityPrefersDefault(t *testing.T) {
	all := []Datasource{
		{ID: 1, UID: "P1", Name: "Prometheus main", Type: "prometheus", IsDefault: true},
		{ID: 2, UID: "P2", Name: "Prometheus long-term", Type: "prometheus"},
	}

	ds, err := findDatasource(all, "prometheus", "prometheus")
	if err != nil {
		t.Fatalf("findDatasource: %v", err)
	}
	if ds.UID != "P1" {
		t.Errorf("uid = %q, want the default datasource P1", ds.UID)
	}

	// With no default to fall back on, guessing is not acceptable.
	all[0].IsDefault = false
	if _, err := findDatasource(all, "prometheus", "prometheus"); err == nil {
		t.Error("expected an ambiguity error when no candidate is the default")
	}
}

// A Grafana that stops serving numeric ids must not degrade selection: id 0
// should never match, and the listing order must stay stable.
func TestFindDatasourceWithoutNumericIDs(t *testing.T) {
	all := []Datasource{
		{UID: "AAA", Name: "Prometheus", Type: "prometheus"},
		{UID: "BBB", Name: "Loki", Type: "loki"},
	}

	if _, err := findDatasource(all, "0", ""); err == nil {
		t.Error("selector \"0\" must not match a datasource with an absent id")
	}
	ds, err := findDatasource(all, "AAA", "prometheus")
	if err != nil || ds.UID != "AAA" {
		t.Fatalf("uid lookup without numeric ids: ds=%v err=%v", ds, err)
	}
}

func TestListDatasourcesSortedByName(t *testing.T) {
	_, gc := newFakeGrafana(t)

	ds, err := gc.ListDatasources()
	if err != nil {
		t.Fatalf("ListDatasources: %v", err)
	}
	var names []string
	for _, d := range ds {
		names = append(names, d.Name)
	}
	want := "Google Cloud Monitoring,Loki,Loki long-term,Prometheus,Tempo"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Error reporting
// ---------------------------------------------------------------------------

func TestHTTPErrorMessages(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		errHas  string
	}{
		{
			name:   "unauthorized names the token variable",
			status: http.StatusUnauthorized, body: `{"message":"Unauthorized"}`,
			errHas: "GRAFANA_TOKEN",
		},
		{
			name:   "forbidden names the token variable",
			status: http.StatusForbidden, body: `{"message":"Forbidden"}`,
			errHas: "Grafana auth failed (HTTP 403)",
		},
		{
			name:    "iap rejection is distinguished from a grafana rejection",
			status:  http.StatusUnauthorized,
			headers: map[string]string{"X-Goog-Iap-Generated-Response": "true"},
			body:    "Unauthorized",
			errHas:  "IAP authentication failed",
		},
		{
			name:   "not found is reported verbatim",
			status: http.StatusNotFound, body: `{"message":"Not found"}`,
			errHas: `HTTP 404: {"message":"Not found"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			t.Setenv("GRAFANA_URL", srv.URL)
			t.Setenv("GRAFANA_TOKEN", "test-token")
			gc, err := NewGrafanaClient()
			if err != nil {
				t.Fatalf("NewGrafanaClient: %v", err)
			}
			_, err = gc.ListDatasources()
			requireErrContains(t, err, tt.errHas)
		})
	}
}

// Tempo takes epoch seconds and Grafana's /api/ds/query takes epoch millis;
// neither parses RFC3339. An accepted-but-unconverted time argument therefore
// reached them as an opaque string.
func TestAbsoluteTimesReachBackendsAsEpochs(t *testing.T) {
	f, gc := newFakeGrafana(t)
	const (
		rfc3339 = "2026-07-27T10:00:00Z"
		epoch   = "1785146400"
		millis  = "1785146400000"
		nanos   = "1785146400000000000"
	)

	t.Run("tempo search", func(t *testing.T) {
		if _, err := gc.TempoSearch("TEMPOUID", "", parseTimeFlag(rfc3339), parseTimeFlag(rfc3339), 5); err != nil {
			t.Fatalf("TempoSearch: %v", err)
		}
		q := f.last(t).Query
		if q.Get("start") != epoch || q.Get("end") != epoch {
			t.Errorf("tempo received start=%q end=%q, want %q (unix seconds)", q.Get("start"), q.Get("end"), epoch)
		}
	})

	t.Run("gcm query", func(t *testing.T) {
		if _, err := gc.GCMQuery("GCMUID", "p", "up", parseTimeMS(rfc3339), parseTimeMS(rfc3339), "60s", ""); err != nil {
			t.Fatalf("GCMQuery: %v", err)
		}
		var payload struct{ From, To string }
		if err := json.Unmarshal([]byte(f.last(t).Body), &payload); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if payload.From != millis || payload.To != millis {
			t.Errorf("gcm received from=%q to=%q, want %q (unix millis)", payload.From, payload.To, millis)
		}
	})

	t.Run("prometheus range", func(t *testing.T) {
		if _, err := gc.PromQueryRange("PROMUID", "up", parseTimeFlag(rfc3339), parseTimeFlag(rfc3339), "60s", ""); err != nil {
			t.Fatalf("PromQueryRange: %v", err)
		}
		if got := f.last(t).Query.Get("start"); got != epoch {
			t.Errorf("prometheus received start=%q, want %q", got, epoch)
		}
	})

	t.Run("loki query", func(t *testing.T) {
		if _, err := gc.LokiQuery("LOKIUID", `{a="b"}`, parseTimeNano(rfc3339), parseTimeNano(rfc3339), 5, "", ""); err != nil {
			t.Fatalf("LokiQuery: %v", err)
		}
		if got := f.last(t).Query.Get("start"); got != nanos {
			t.Errorf("loki received start=%q, want %q (unix nanos)", got, nanos)
		}
	})
}
