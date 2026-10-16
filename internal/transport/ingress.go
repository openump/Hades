package transport

import (
	"fmt"
	"net/http"
	"strings"
)

// IngressPolicy authorizes mTLS client identities for observation writes and
// health reads, with optional per-writer asset scoping ("<identity>@<asset_id>").
type IngressPolicy struct {
	Writers map[string]map[string]bool
	Readers map[string]bool
}

func DefaultIngressPolicy() *IngressPolicy {
	p, _ := NewIngressPolicy([]string{"gateway-press-04"}, []string{"admin"})
	return p
}

func ParseCSVIdentities(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func NewIngressPolicy(writers, readers []string) (*IngressPolicy, error) {
	if len(writers) == 0 || len(readers) == 0 {
		return nil, fmt.Errorf("ingress policy requires at least one writer and one reader identity")
	}
	wmap := make(map[string]map[string]bool, len(writers))
	for _, entry := range writers {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("empty writer identity")
		}
		identity, assetID, scoped := strings.Cut(entry, "@")
		if identity == "" || (scoped && assetID == "") {
			return nil, fmt.Errorf("invalid writer identity spec %q", entry)
		}
		assets, exists := wmap[identity]
		if !exists {
			assets = map[string]bool{}
			wmap[identity] = assets
		}
		if scoped && assets != nil {
			assets[assetID] = true
		} else {
			// Unscoped writer grant allows any catalog asset for this identity.
			wmap[identity] = nil
		}
	}
	rmap := make(map[string]bool, len(readers))
	for _, entry := range readers {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("empty reader identity")
		}
		rmap[entry] = true
	}
	return &IngressPolicy{Writers: wmap, Readers: rmap}, nil
}

// PeerIdentities returns the verified client certificate's CommonName, DNS SANs,
// and URI SANs in deterministic order.
func PeerIdentities(r *http.Request) []string {
	if r == nil || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		return nil
	}
	leaf := r.TLS.VerifiedChains[0][0]
	var ids []string
	if leaf.Subject.CommonName != "" {
		ids = append(ids, leaf.Subject.CommonName)
	}
	for _, dns := range leaf.DNSNames {
		if dns != "" {
			ids = append(ids, dns)
		}
	}
	for _, u := range leaf.URIs {
		if u != nil && u.String() != "" {
			ids = append(ids, u.String())
		}
	}
	return ids
}

func (p *IngressPolicy) AuthorizeRequest(r *http.Request) bool {
	if p == nil {
		p = DefaultIngressPolicy()
	}
	ids := PeerIdentities(r)
	if len(ids) == 0 {
		return false
	}
	if r.URL.Path == "/observations" && r.Method == http.MethodPost {
		for _, id := range ids {
			if _, ok := p.Writers[id]; ok {
				return true
			}
		}
		return false
	}
	if r.URL.Path == "/health" && r.Method == http.MethodGet {
		for _, id := range ids {
			if p.Readers[id] {
				return true
			}
		}
		return false
	}
	return false
}

// AuthorizeAsset verifies whether the authenticated mTLS peer is permitted to
// submit observations for assetID. When r has no TLS state (loopback HTTP dev
// mode), AuthorizeAsset returns true.
func (p *IngressPolicy) AuthorizeAsset(r *http.Request, assetID string) bool {
	if r == nil || r.TLS == nil {
		return true
	}
	if p == nil {
		p = DefaultIngressPolicy()
	}
	for _, id := range PeerIdentities(r) {
		assets, ok := p.Writers[id]
		if !ok {
			continue
		}
		if len(assets) == 0 || assets[assetID] {
			return true
		}
	}
	return false
}

func (p *IngressPolicy) Wrap(next http.Handler) http.Handler {
	if p == nil {
		p = DefaultIngressPolicy()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.AuthorizeRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
