// Package mitm intercepts the recording browser's HTTPS traffic. A local
// root CA, created once per installation, signs a leaf certificate for each
// host the browser visits; the proxy presents that leaf to the browser and
// opens its own verified TLS connection to the real origin.
package mitm

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Files in the CA directory. The keys never leave it and never go into a
// recording.
const (
	caCertFile  = "ca.crt"
	caKeyFile   = "ca.key"
	leafKeyFile = "leaf.key"
)

// Leaves live shorter than the 398 days browsers accept, with a day of
// slack for clock skew.
const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 390 * 24 * time.Hour
	clockSlack   = 24 * time.Hour
)

// CA issues leaf certificates. Every leaf shares one key pair, so the
// browser can be told to trust exactly that key with
// --ignore-certificate-errors-spki-list, without touching the operating
// system's trust store.
type CA struct {
	Cert *x509.Certificate
	key  crypto.Signer

	leafKey *ecdsa.PrivateKey

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

// LoadOrCreateCA loads the CA in dir, creating it on first use.
func LoadOrCreateCA(dir string) (*CA, error) {
	ca, err := loadCA(dir)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return ca, err
	}
	return createCA(dir)
}

// ResetCA deletes the CA in dir, so the next LoadOrCreateCA makes a new
// one.
func ResetCA(dir string) error {
	for _, name := range []string{caCertFile, caKeyFile, leafKeyFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func createCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject: pkix.Name{
			CommonName:   "Webshadow Local CA",
			Organization: []string{"Webshadow"},
			// The host name tells installations apart in a certificate viewer.
			OrganizationalUnit: []string{host},
		},
		NotBefore:             now.Add(-clockSlack),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	// Keys first: a crash between writes leaves no certificate, so the next
	// run starts over instead of loading a half-written CA.
	if err := writeKey(filepath.Join(dir, caKeyFile), key); err != nil {
		return nil, err
	}
	if err := writeKey(filepath.Join(dir, leafKeyFile), leafKey); err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeFile(filepath.Join(dir, caCertFile), certPEM, 0o644); err != nil {
		return nil, err
	}
	return newCA(cert, key, leafKey), nil
}

func loadCA(dir string) (*CA, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("mitm: %s is not a PEM certificate", caCertFile)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := readKey(filepath.Join(dir, caKeyFile))
	if err != nil {
		return nil, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, fmt.Errorf("mitm: %s does not match %s", caKeyFile, caCertFile)
	}
	leafKey, err := readKey(filepath.Join(dir, leafKeyFile))
	if err != nil {
		return nil, err
	}
	return newCA(cert, key, leafKey), nil
}

func newCA(cert *x509.Certificate, key crypto.Signer, leafKey *ecdsa.PrivateKey) *CA {
	return &CA{Cert: cert, key: key, leafKey: leafKey, leaves: map[string]*tls.Certificate{}}
}

// Fingerprint is the hex SHA-256 of the CA certificate.
func (ca *CA) Fingerprint() string {
	sum := sha256.Sum256(ca.Cert.Raw)
	return hex.EncodeToString(sum[:])
}

// LeafSPKIHash is the base64 SHA-256 of the shared leaf public key, the
// form Chromium's --ignore-certificate-errors-spki-list takes.
func (ca *CA) LeafSPKIHash() string {
	der, err := x509.MarshalPKIXPublicKey(&ca.leafKey.PublicKey)
	if err != nil {
		panic(err) // a P-256 key always marshals
	}
	sum := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Leaf returns a certificate for host, a DNS name or an IP address, issued
// on first use and cached for the life of the CA.
func (ca *CA) Leaf(host string) (*tls.Certificate, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return nil, errors.New("mitm: leaf for empty host")
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if c, ok := ca.leaves[host]; ok && time.Now().Before(c.Leaf.NotAfter) {
		return c, nil
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-clockSlack),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &ca.leafKey.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{
		Certificate: [][]byte{der, ca.Cert.Raw},
		PrivateKey:  ca.leafKey,
		Leaf:        leaf,
	}
	ca.leaves[host] = c
	return c, nil
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return n.Add(n, big.NewInt(1))
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writeFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}

func readKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("mitm: %s is not a PEM private key", filepath.Base(path))
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("mitm: %s is not an ECDSA key", filepath.Base(path))
	}
	return key, nil
}

// writeFile writes a new file with mode perm, replacing any old one.
func writeFile(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Chmod(perm), tmp.Sync(), tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
