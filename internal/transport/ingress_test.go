package transport

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func requestWithCert(method, path, cn string, dns []string, uris []string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	cert := &x509.Certificate{
		Subject:  pkix.Name{CommonName: cn},
		DNSNames: dns,
	}
	for _, raw := range uris {
		u, _ := url.Parse(raw)
		cert.URIs = append(cert.URIs, u)
	}
	req.TLS = &tls.ConnectionState{
		VerifiedChains: [][]*x509.Certificate{{cert}},
	}
	return req
}

func TestIngressPolicyConfigurableWritersReadersAndSANs(t *testing.T) {
	policy, err := NewIngressPolicy(
		[]string{"gateway-press-04@press-04", "urn:openump:gateway:oven-01@oven-01", "gateway-site"},
		[]string{"admin", "health-monitor.local"},
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := policy.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Allowed writer by CN
	rec := httptest.NewRecorder()
	req := requestWithCert(http.MethodPost, "/observations", "gateway-press-04", nil, nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for CN writer, got %d", rec.Code)
	}
	if !policy.AuthorizeAsset(req, "press-04") || policy.AuthorizeAsset(req, "oven-01") {
		t.Fatalf("expected press-04 writer to be scoped only to press-04")
	}

	// Allowed writer by URI SAN
	rec = httptest.NewRecorder()
	req = requestWithCert(http.MethodPost, "/observations", "other-cn", nil, []string{"urn:openump:gateway:oven-01"})
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for URI SAN writer, got %d", rec.Code)
	}
	if !policy.AuthorizeAsset(req, "oven-01") || policy.AuthorizeAsset(req, "press-04") {
		t.Fatalf("expected URI SAN writer to be scoped only to oven-01")
	}

	// Unscoped writer can write to any catalog asset
	req = requestWithCert(http.MethodPost, "/observations", "gateway-site", nil, nil)
	if !policy.AuthorizeAsset(req, "press-04") || !policy.AuthorizeAsset(req, "oven-01") {
		t.Fatalf("expected unscoped writer to authorize any asset")
	}

	// Allowed reader by DNS SAN
	rec = httptest.NewRecorder()
	req = requestWithCert(http.MethodGet, "/health", "other", []string{"health-monitor.local"}, nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for DNS SAN reader, got %d", rec.Code)
	}

	// Reader cannot POST /observations
	rec = httptest.NewRecorder()
	req = requestWithCert(http.MethodPost, "/observations", "admin", nil, nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when reader posts observations, got %d", rec.Code)
	}

	// Writer cannot GET /health
	rec = httptest.NewRecorder()
	req = requestWithCert(http.MethodGet, "/health", "gateway-press-04", nil, nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when writer reads health, got %d", rec.Code)
	}
}
