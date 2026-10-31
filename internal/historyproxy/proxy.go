// Package historyproxy restricts the historian's exposed routes by verified identity.
package historyproxy

import (
	"net/http"

	"github.com/openump/Hades/internal/transport"
)

func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := transport.Peer(r)
		read := r.URL.Path == "/api/v1/query" || r.URL.Path == "/api/v1/query_range" || r.URL.Path == "/api/v1/export"
		allowed := peer == "history-adapter" && r.Method == http.MethodPost && r.URL.Path == "/api/v1/import/prometheus" || (peer == "consumer-primary" || peer == "consumer-secondary") && read && (r.Method == http.MethodGet || r.Method == http.MethodPost) ||
			// hades-mcp builds bounded single-series range queries itself; it gets
			// that one route only, never export or instant queries.
			peer == "hades-mcp" && r.Method == http.MethodGet && r.URL.Path == "/api/v1/query_range" ||
			peer == "admin" && r.Method == http.MethodGet && (r.URL.Path == "/health" || r.URL.Path == "/metrics")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !allowed {
			http.Error(w, "forbidden", 403)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		next.ServeHTTP(w, r)
	})
}
