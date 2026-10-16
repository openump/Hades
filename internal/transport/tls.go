// Package transport loads explicit mTLS trust and rejects cleartext downgrades.
package transport

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
)

type Paths struct{ CA, Cert, Key string }

func Flags() *Paths {
	p := &Paths{}
	flag.StringVar(&p.CA, "tls-ca", "", "trusted PEM CA file")
	flag.StringVar(&p.Cert, "tls-cert", "", "local certificate file")
	flag.StringVar(&p.Key, "tls-key", "", "private key file")
	return p
}
func load(p Paths) (*tls.Config, error) {
	if p.CA == "" || p.Cert == "" || p.Key == "" {
		return nil, fmt.Errorf("all three TLS files are required")
	}
	raw, err := os.ReadFile(p.CA)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("invalid trusted CA")
	}
	certificate, err := tls.LoadX509KeyPair(p.Cert, p.Key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ClientCAs: pool, Certificates: []tls.Certificate{certificate}}, nil
}
func Client(p Paths) (*tls.Config, error) {
	if p == (Paths{}) {
		return nil, nil
	}
	return load(p)
}
func Server(p Paths) (*tls.Config, error) {
	cfg, err := load(p)
	if err != nil {
		return nil, err
	}
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	return cfg, nil
}
func BrokerURL(address string, cfg *tls.Config) (*url.URL, error) {
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid broker URL")
	}
	if cfg != nil {
		if u.Scheme != "tls" {
			return nil, fmt.Errorf("TLS credentials require tls:// broker; no downgrade")
		}
		return u, nil
	}
	if u.Scheme != "mqtt" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return nil, fmt.Errorf("cleartext broker must be loopback mqtt://")
	}
	return u, nil
}
func Peer(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return ""
	}
	return r.TLS.VerifiedChains[0][0].Subject.CommonName
}
func Ingress(next http.Handler) http.Handler {
	return DefaultIngressPolicy().Wrap(next)
}
