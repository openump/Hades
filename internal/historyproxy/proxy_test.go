package historyproxy

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoleAndManagementIsolation(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for _, test := range []struct {
		peer, method, path string
		status             int
	}{{"history-adapter", "POST", "/api/v1/import/prometheus", 204}, {"consumer-primary", "GET", "/api/v1/query_range", 204}, {"consumer-secondary", "POST", "/api/v1/import/prometheus", 403}, {"history-adapter", "GET", "/api/v1/admin/tsdb/delete_series", 403}, {"consumer-primary", "GET", "/metrics", 403}, {"admin", "GET", "/health", 204}, {"outsider", "GET", "/api/v1/query", 403}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: test.peer}}}}}
		w := httptest.NewRecorder()
		Guard(next).ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(test, w.Code)
		}
	}
}
