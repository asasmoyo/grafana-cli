package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
)

// iapTransport sets IAP ID token via Proxy-Authorization,
// leaving Authorization free for the backend (Grafana).
type iapTransport struct {
	iapToken string
	base     http.RoundTripper
}

func (t *iapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Proxy-Authorization", "Bearer "+t.iapToken)
	return t.base.RoundTrip(req)
}

// getIAPToken uses gcloud to mint an ID token for the given IAP client ID,
// impersonating the specified service account.
func getIAPToken(ctx context.Context, clientID, serviceAccount string) (string, error) {
	cmd := exec.CommandContext(ctx, "gcloud", "auth", "print-identity-token",
		"--audiences="+clientID,
		"--include-email",
		"--impersonate-service-account="+serviceAccount,
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("gcloud auth print-identity-token failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("gcloud auth print-identity-token failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
