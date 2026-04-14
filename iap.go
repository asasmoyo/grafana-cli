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

// looksLikeJWT checks whether s has the three-part base64url structure of a JWT.
func looksLikeJWT(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 {
			return false
		}
		for _, c := range p {
			if !isBase64URL(c) {
				return false
			}
		}
	}
	return true
}

func isBase64URL(c rune) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '='
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
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("gcloud auth print-identity-token returned empty output")
	}
	if !looksLikeJWT(token) {
		preview := token
		if len(preview) > 40 {
			preview = preview[:40] + "..."
		}
		return "", fmt.Errorf("gcloud auth print-identity-token returned non-JWT output: %q", preview)
	}
	return token, nil
}
