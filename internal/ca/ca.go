// Package ca owns the local certificate authority used for HTTPS interception.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	keyFile      = "ca-key.pem"
	certFile     = "ca-cert.pem"
	leafCacheCap = 1024
	leafLifetime = 7 * 24 * time.Hour
	leafRenewIn  = time.Hour
)

// ErrInsecurePerms reports a CA key file readable by group or others.
var ErrInsecurePerms = errors.New("ca: key file permissions too open")

// Authority signs per-host leaf certificates on demand.
type Authority struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	leafKey *ecdsa.PrivateKey
	now     func() time.Time

	mu     sync.Mutex
	leaves *lru[*tls.Certificate]
}

// DefaultDir returns the CA directory under the user config dir.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("ca: locate config dir: %w", err)
	}
	return filepath.Join(base, "burpui"), nil
}

// Generate creates a fresh in-memory authority valid for ten years.
func Generate() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "burpui local CA", Organization: []string{"burpui"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: sign root: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse root: %w", err)
	}
	return newAuthority(cert, key)
}

func newAuthority(cert *x509.Certificate, key *ecdsa.PrivateKey) (*Authority, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generate leaf key: %w", err)
	}
	return &Authority{
		cert: cert, key: key, leafKey: leafKey, now: time.Now,
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}),
		leaves:  newLRU[*tls.Certificate](leafCacheCap),
	}, nil
}

// Save persists the key (0600) and certificate (0644) into dir.
func (a *Authority) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ca: create dir: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(a.key)
	if err != nil {
		return fmt.Errorf("ca: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(dir, keyFile), keyPEM, 0o600); err != nil {
		return fmt.Errorf("ca: write key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, certFile), a.certPEM, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("ca: write cert: %w", err)
	}
	return nil
}

// Load reads an authority saved by Save and rejects a key readable by others.
func Load(dir string) (*Authority, error) {
	keyPath := filepath.Join(dir, keyFile)
	info, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: stat key: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s is %o", ErrInsecurePerms, keyPath, info.Mode().Perm())
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ca: read key: %w", err)
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, certFile))
	if err != nil {
		return nil, fmt.Errorf("ca: read cert: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	cb, _ := pem.Decode(certPEM)
	if kb == nil || cb == nil {
		return nil, errors.New("ca: invalid PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse key: %w", err)
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse cert: %w", err)
	}
	return newAuthority(cert, key)
}

// LoadOrCreate loads the authority in dir, creating and saving one when absent.
func LoadOrCreate(dir string) (a *Authority, created bool, err error) {
	a, err = Load(dir)
	if err == nil {
		return a, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if a, err = Generate(); err != nil {
		return nil, false, err
	}
	if err = a.Save(dir); err != nil {
		return nil, false, err
	}
	return a, true, nil
}

// CertPEM returns the root certificate in PEM form.
func (a *Authority) CertPEM() []byte { return a.certPEM }

// Pool returns a pool trusting only this authority.
func (a *Authority) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(a.cert)
	return p
}

// Fingerprint returns the SHA-256 fingerprint of the root certificate.
func (a *Authority) Fingerprint() string {
	sum := sha256.Sum256(a.cert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// Leaf returns a certificate for host, minted once and cached in an LRU.
func (a *Authority) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, errors.New("ca: empty host")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.leaves.get(host); ok && c.Leaf.NotAfter.Sub(a.now()) > leafRenewIn {
		return c, nil
	}
	c, err := a.mint(host)
	if err != nil {
		return nil, err
	}
	a.leaves.put(host, c)
	return c, nil
}

func (a *Authority) mint(host string) (*tls.Certificate, error) {
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := a.now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, a.cert, &a.leafKey.PublicKey, a.key)
	if err != nil {
		return nil, fmt.Errorf("ca: sign leaf for %s: %w", host, err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: parse leaf: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: a.leafKey, Leaf: leaf}, nil
}

// TrustInstructions explains how to trust the root certificate stored at certPath.
func TrustInstructions(certPath, fingerprint string) string {
	return fmt.Sprintf(`CA certificate: %s
SHA-256 fingerprint: %s

Trust it only on machines you test from, and remove it afterwards:
  Linux (Debian/Ubuntu): sudo cp %s /usr/local/share/ca-certificates/burpui.crt && sudo update-ca-certificates
  Linux (Arch): sudo trust anchor %s
  macOS: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s
  Windows: certutil -user -addstore Root %s
  curl: curl --cacert %s --proxy http://127.0.0.1:8080 https://example.com
`, certPath, fingerprint, certPath, certPath, certPath, certPath, certPath)
}

// CertPath returns where the root certificate lives inside dir.
func CertPath(dir string) string { return filepath.Join(dir, certFile) }

func newSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("ca: serial: %w", err)
	}
	return n, nil
}
