package actuator

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/openump/Hades/internal/storage"
)

type AuditRecord struct {
	Sequence  uint64          `json:"sequence"`
	Previous  string          `json:"previous"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body"`
	Signature string          `json:"signature"`
}
type Audit struct {
	store    *storage.Store
	key      ed25519.PrivateKey
	sequence uint64
	hash     string
	Records  []AuditRecord
}

func OpenAudit(dir string, key ed25519.PrivateKey) (*Audit, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid audit key")
	}
	store, err := storage.Open(dir, 10000, 64*1024*1024, 64*1024*1024)
	if err != nil {
		return nil, err
	}
	a := &Audit{store: store, key: key}
	ids := make([]string, 0, len(store.Records))
	for id := range store.Records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, _, err := store.Read(id)
		if err != nil {
			store.Close()
			return nil, err
		}
		var r AuditRecord
		if json.Unmarshal(raw, &r) != nil || r.Sequence != a.sequence+1 || id != fmt.Sprintf("%020d", r.Sequence) || r.Previous != a.hash {
			store.Close()
			return nil, fmt.Errorf("audit chain discontinuity")
		}
		signature, err := base64.StdEncoding.Strict().DecodeString(r.Signature)
		if err != nil {
			store.Close()
			return nil, err
		}
		r.Signature = ""
		body, _ := json.Marshal(r)
		if !ed25519.Verify(key.Public().(ed25519.PublicKey), append([]byte("OpenUMP audit v1\n"), body...), signature) {
			store.Close()
			return nil, fmt.Errorf("audit signature mismatch")
		}
		r.Signature = base64.StdEncoding.EncodeToString(signature)
		a.hash = fmt.Sprintf("%x", sha256.Sum256(raw))
		a.sequence = r.Sequence
		a.Records = append(a.Records, r)
	}
	return a, nil
}
func (a *Audit) Append(kind string, body any) error {
	// Reserve room for a post-I/O receipt; a full audit must refuse before writing.
	if kind == "intent" && (len(a.store.Records) > 9996 || a.store.Bytes > 64*1024*1024-256*1024) {
		return storage.ErrFull
	}
	if kind == "intent" {
		if err := a.store.Available(256 * 1024); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	r := AuditRecord{Sequence: a.sequence + 1, Previous: a.hash, Kind: kind, Body: raw}
	unsigned, _ := json.Marshal(r)
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(a.key, append([]byte("OpenUMP audit v1\n"), unsigned...)))
	encoded, _ := json.Marshal(r)
	if err = a.store.Put(fmt.Sprintf("%020d", r.Sequence), encoded); err != nil {
		return err
	}
	a.sequence = r.Sequence
	a.hash = fmt.Sprintf("%x", sha256.Sum256(encoded))
	a.Records = append(a.Records, r)
	return nil
}
func (a *Audit) Close() error { return a.store.Close() }
