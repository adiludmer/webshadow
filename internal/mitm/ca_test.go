package mitm

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLeafChainsToCA(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)

	for _, host := range []string{"www.amazon.com", "127.0.0.1", "::1"} {
		c, err := ca.Leaf(host)
		if err != nil {
			t.Fatalf("Leaf(%s): %v", host, err)
		}
		if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err != nil {
			t.Errorf("leaf for %s does not verify: %v", host, err)
		}
		if err := c.Leaf.CheckSignatureFrom(ca.Cert); err != nil {
			t.Errorf("leaf for %s not signed by CA: %v", host, err)
		}
		if ip := net.ParseIP(host); ip != nil {
			if len(c.Leaf.IPAddresses) != 1 || !c.Leaf.IPAddresses[0].Equal(ip) {
				t.Errorf("leaf for %s has IP SANs %v", host, c.Leaf.IPAddresses)
			}
		} else if len(c.Leaf.DNSNames) != 1 || c.Leaf.DNSNames[0] != host {
			t.Errorf("leaf for %s has DNS SANs %v", host, c.Leaf.DNSNames)
		}
	}
	c, _ := ca.Leaf("www.amazon.com")
	if _, err := c.Leaf.Verify(x509.VerifyOptions{DNSName: "evil.example", Roots: roots}); err == nil {
		t.Error("leaf verified for a host it was not issued for")
	}
	if again, _ := ca.Leaf("WWW.Amazon.com."); again != c {
		t.Error("leaf not cached by normalized host name")
	}
}

func TestLeafSPKIHashMatchesEveryLeaf(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"a.test", "b.test"} {
		c, _ := ca.Leaf(host)
		sum := sha256.Sum256(c.Leaf.RawSubjectPublicKeyInfo)
		if got := base64.StdEncoding.EncodeToString(sum[:]); got != ca.LeafSPKIHash() {
			t.Errorf("%s SPKI hash = %s, want %s", host, got, ca.LeafSPKIHash())
		}
	}
}

func TestCAPersistsAndResets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() != b.Fingerprint() || a.LeafSPKIHash() != b.LeafSPKIHash() {
		t.Error("reloading produced a different CA")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{caKeyFile, leafKeyFile} {
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("%s mode = %v, want 0600", name, perm)
			}
		}
		if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
			t.Errorf("CA dir mode = %v, want 0700", info.Mode().Perm())
		}
	}
	if err := ResetCA(dir); err != nil {
		t.Fatal(err)
	}
	c, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Fingerprint() == a.Fingerprint() {
		t.Error("reset kept the old CA")
	}
}

func TestLoadRejectsMismatchedKey(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateCA(dir); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if _, err := LoadOrCreateCA(other); err != nil {
		t.Fatal(err)
	}
	key, _ := os.ReadFile(filepath.Join(other, caKeyFile))
	os.WriteFile(filepath.Join(dir, caKeyFile), key, 0o600)
	if _, err := LoadOrCreateCA(dir); err == nil {
		t.Error("loaded a CA whose key does not match its certificate")
	}
}

// TestTLSHandshake checks a client that trusts only the CA completes a
// handshake against a server presenting an issued leaf.
func TestTLSHandshake(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			return ca.Leaf(hello.ServerName)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: "shop.test", RootCAs: roots})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	conn.Close()
}
