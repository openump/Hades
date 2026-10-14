package transport

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCleartextOnlyLoopbackAndTLSNeverFallsBack(t *testing.T) {
	for _, address := range []string{"mqtt://example.com:1883", "mqtt://user:secret@127.0.0.1:1883", "http://127.0.0.1:1883"} {
		if _, err := BrokerURL(address, nil); err == nil {
			t.Fatal("accepted", address)
		}
	}
	if _, err := BrokerURL("tls://example.com:8883", nil); err == nil {
		t.Fatal("TLS without trust accepted")
	}
	if _, err := BrokerURL("mqtt://127.0.0.1:1883", &tls.Config{}); err == nil {
		t.Fatal("downgrade accepted")
	}
	if _, err := BrokerURL("tls://example.com:8883", &tls.Config{MinVersion: tls.VersionTLS12}); err != nil {
		t.Fatal(err)
	}
	if _, err := Client(Paths{CA: "missing"}); err == nil {
		t.Fatal("partial credentials accepted")
	}
}

func TestIngressCannotUseUnverifiedIdentity(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	for _, test := range []struct {
		peer, method, path string
		allowed            bool
	}{{"gateway-press-04", "POST", "/observations", true}, {"consumer-primary", "POST", "/observations", false}, {"admin", "GET", "/health", true}, {"gateway-press-04", "GET", "/health", false}, {"admin", "POST", "/observations", false}, {"", "POST", "/observations", false}} {
		r := httptest.NewRequest(test.method, test.path, nil)
		if test.peer != "" {
			r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{Subject: pkix.Name{CommonName: test.peer}}}}}
		}
		w := httptest.NewRecorder()
		Ingress(next).ServeHTTP(w, r)
		if (w.Code == 204) != test.allowed {
			t.Fatal(test, w.Code)
		}
	}
}
