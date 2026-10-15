package transport

import (
	"fmt"
	"net/http"
	"regexp"
)

// IngressPolicy authorizes exact verified certificate CNs, never unverified
// request headers or ambient wildcard identities. It does not authorize assets;
// the normalizer/catalog must still validate the payload's context and topic.
type IngressPolicy struct{ writers, readers map[string]bool }

func NewIngressPolicy(writers, readers []string) (*IngressPolicy, error) {
	p := &IngressPolicy{writers: map[string]bool{}, readers: map[string]bool{}}
	valid := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	for index, list := range [][]string{writers, readers} {
		if len(list) > 64 {
			return nil, fmt.Errorf("too many ingress identities")
		}
		target := p.writers
		if index == 1 {
			target = p.readers
		}
		for _, name := range list {
			if !valid.MatchString(name) || target[name] {
				return nil, fmt.Errorf("invalid or duplicate ingress identity")
			}
			target[name] = true
		}
	}
	if len(p.writers) == 0 {
		return nil, fmt.Errorf("at least one explicit ingress writer required")
	}
	return p, nil
}
func (p *IngressPolicy) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := Peer(r)
		allowed := p != nil && ((r.URL.Path == "/observations" && r.Method == http.MethodPost && p.writers[peer]) || (r.URL.Path == "/health" && r.Method == http.MethodGet && p.readers[peer]))
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
