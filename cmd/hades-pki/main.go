// hades-pki creates disposable local verification identities, not a production CA.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	out := flag.String("out", "runtime/security/v1", "new private output directory")
	tests := flag.Bool("test-identities", false, "also issue an unprivileged negative-test identity")
	flag.Parse()
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(*out, 0700); err != nil {
		return fmt.Errorf("output must be a new directory: %w", err)
	}
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial := func() *big.Int {
		n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			panic(err)
		}
		return n
	}
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "hades-local-verification-ca"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	ca, err = x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	write := func(name string, der []byte, kind string, mode os.FileMode) error {
		return os.WriteFile(filepath.Join(*out, name), pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), mode)
	}
	if err = write("ca.crt", caDER, "CERTIFICATE", 0644); err != nil {
		return err
	}
	caKey, _ := x509.MarshalPKCS8PrivateKey(key)
	if err = write("ca.key", caKey, "PRIVATE KEY", 0600); err != nil {
		return err
	}
	identities := []string{"broker", "ingress", "history-proxy", "gateway-press-04", "normalizer", "consumer-primary", "consumer-secondary", "history-adapter", "transfer-adapter", "cloud-receiver", "admin"}
	if *tests {
		identities = append(identities, "outsider", "hades-actuator", "command-proposer")
	}
	for _, name := range identities {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		leaf := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if name == "broker" || name == "ingress" || name == "history-proxy" || name == "cloud-receiver" {
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			leaf.DNSNames = []string{"localhost", name}
			leaf.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			return err
		}
		if err = write(name+".crt", der, "CERTIFICATE", 0644); err != nil {
			return err
		}
		encoded, _ := x509.MarshalPKCS8PrivateKey(leafKey)
		if err = write(name+".key", encoded, "PRIVATE KEY", 0600); err != nil {
			return err
		}
	}
	fmt.Println("created short-lived local verification identities; keep private files outside Git")
	return nil
}
