package inspect

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed web
var webFiles embed.FS

// contentSecurityPolicy forbids every external origin: the console works fully
// offline on an air-gapped shopfloor and can load nothing from a CDN.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// NewHandler serves the read-only console. Every route is GET or HEAD only:
// the method check wraps the whole mux, so no route can accept a write.
// allowedHosts lists the Host header values accepted (for example
// "127.0.0.1:18091" and "localhost:18091"); any other host is refused, which
// blocks DNS-rebinding pages from reading the console through a browser.
func NewHandler(model *Model, b *Broadcaster, heartbeat time.Duration, allowedHosts ...string) http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	mux := http.NewServeMux()
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(static))))
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/ui/api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(model.Snapshot())
	})
	mux.HandleFunc("/ui/stream", func(w http.ResponseWriter, r *http.Request) {
		stream(w, r, model, b, heartbeat)
	})
	return hostGuard(allowedHosts, readOnly(mux))
}

// hostGuard refuses requests whose Host header is not an expected loopback
// name. A DNS-rebinding page resolves its own hostname to 127.0.0.1; the
// browser then sends that hostname, which is not on the list.
func hostGuard(allowed []string, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	ok := map[string]bool{}
	for _, h := range allowed {
		ok[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[strings.ToLower(r.Host)] {
			http.Error(w, "unexpected host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LoopbackHosts returns the Host values a browser sends for a loopback listen
// address: the address itself and localhost on the same port.
func LoopbackHosts(listen string) []string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return []string{listen}
	}
	return []string{listen, "localhost:" + port, "127.0.0.1:" + port, "[::1]:" + port}
}

// readOnly admits only GET and HEAD and sets defensive headers on every
// response.
func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "read-only console", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stream pushes a snapshot on every model change (coalesced) and a heartbeat,
// over Server-Sent Events. A slow client misses intermediate updates rather
// than blocking anyone; a client that stops reading is dropped by its write
// deadline.
func stream(w http.ResponseWriter, r *http.Request, model *Model, b *Broadcaster, heartbeat time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	signal, done, ok := b.Subscribe()
	if !ok {
		w.Header().Set("Retry-After", "5")
		http.Error(w, fmt.Sprintf("at most %d live inspector streams", MaxStreams), http.StatusServiceUnavailable)
		return
	}
	defer done()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	controller := http.NewResponseController(w)
	send := func(event string, payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	if send("snapshot", model.Snapshot()) != nil {
		return
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-signal:
			if send("snapshot", model.Snapshot()) != nil {
				return
			}
		case <-ticker.C:
			if send("heartbeat", map[string]any{"at": time.Now().UTC()}) != nil {
				return
			}
		}
	}
}

// Serve runs the console until ctx ends. Request contexts derive from ctx, so
// open SSE streams end immediately on shutdown instead of holding it up.
func Serve(ctx context.Context, srv *http.Server) error {
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	case err := <-errs:
		return err
	}
}
