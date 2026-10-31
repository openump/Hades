package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServicesRefuseTLS12: the shared mTLS configuration negotiates TLS 1.3
// and refuses a TLS 1.2-only peer (IEC 62443-4-2 CR 1.1 / CR 3.1).
func TestServicesRefuseTLS12(t *testing.T) {
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	write := func(name string, block *pem.Block) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, pem.EncodeToMemory(block), 0o600)
		return p
	}
	caPath := write("ca.crt", &pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	issue := func(name string, serial int64) Paths {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		keyDER, _ := x509.MarshalECPrivateKey(key)
		return Paths{CA: caPath, Cert: write(name+".crt", &pem.Block{Type: "CERTIFICATE", Bytes: der}), Key: write(name+".key", &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})}
	}
	serverCfg, err := Server(issue("server", 2))
	if err != nil {
		t.Fatal(err)
	}
	clientCfg, err := Client(issue("client", 3))
	if err != nil {
		t.Fatal(err)
	}
	if serverCfg.MinVersion != tls.VersionTLS13 || clientCfg.MinVersion != tls.VersionTLS13 {
		t.Fatal("service TLS minimum must be 1.3")
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	modern := clientCfg.Clone()
	modern.ServerName = "127.0.0.1"
	conn, err := tls.Dial("tcp", listener.Addr().String(), modern)
	if err != nil {
		t.Fatal("a TLS 1.3 client must connect:", err)
	}
	if v := conn.ConnectionState().Version; v != tls.VersionTLS13 {
		t.Fatalf("negotiated 0x%x, want TLS 1.3", v)
	}
	conn.Close()
	legacy := clientCfg.Clone()
	legacy.ServerName = "127.0.0.1"
	legacy.MinVersion, legacy.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	if c, err := tls.Dial("tcp", listener.Addr().String(), legacy); err == nil {
		c.Close()
		t.Fatal("a TLS 1.2-only peer must be refused")
	}
}
