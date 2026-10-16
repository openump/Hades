package transport

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfiguredIngressFailsClosed(t *testing.T) {
	policy, err := NewIngressPolicy([]string{"gateway-east"}, []string{"operator-east"})
	if err != nil {
		t.Fatal(err)
	}
	handler := policy.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		peer, path, method string
		want               int
	}{{"gateway-east", "/observations", "POST", 204}, {"gateway-press-04", "/observations", "POST", 403}, {"operator-east", "/health", "GET", 204}, {"gateway-east", "/health", "GET", 403}, {"operator-east", "/observations", "POST", 403}, {"", "/health", "GET", 403}} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.peer != "" {
			r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: tc.peer}}}}}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(tc, w.Code)
		}
	}
	if _, err = NewIngressPolicy([]string{"*"}, nil); err == nil {
		t.Fatal("wildcard identity accepted")
	}
}
