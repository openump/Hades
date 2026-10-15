package opcuapilot

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// InitPKI creates short-lived pinned self-signed application identities. No
// production CA is involved. The outsider certificate exists only for negatives.
func InitPKI(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	for _, role := range []string{"server", "client", "outsider"} {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return err
		}
		uri := ClientURI
		if role == "server" {
			uri = ServerURI
		}
		if role == "outsider" {
			uri = "urn:openump:opcua:outsider"
		}
		parsed, _ := url.Parse(uri)
		now := time.Now()
		cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "hades-opcua-" + role}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, URIs: []*url.URL{parsed}, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment | x509.KeyUsageKeyEncipherment | x509.KeyUsageDataEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, role+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, role+".key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
			return err
		}
	}
	return nil
}
func Certificate(path, uri string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("missing certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || public.N.BitLen() < 2048 || public.N.BitLen() > 4096 {
		return nil, fmt.Errorf("RSA 2048..4096 application certificate required")
	}
	if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) || len(cert.URIs) != 1 || cert.URIs[0].String() != uri {
		return nil, fmt.Errorf("certificate time or application URI mismatch")
	}
	if err = cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return nil, err
	}
	return cert, nil
}
func SameIdentity(raw []byte, expected *x509.Certificate) bool { return bytes.Equal(raw, expected.Raw) }
