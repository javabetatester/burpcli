package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Store struct {
	Dir string

	mu        sync.Mutex
	caCert    *x509.Certificate
	caKey     *ecdsa.PrivateKey
	caCertPEM []byte

	leafMu   sync.Mutex
	leafCert map[string]*tlsCert
}

type tlsCert struct {
	certPEM []byte
	keyPEM  []byte
	cert    *x509.Certificate
}

func LoadOrCreate(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("ca dir vazio")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	certPath := filepath.Join(dir, "ca.crt.pem")
	keyPath := filepath.Join(dir, "ca.key.pem")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		caCert, err := parseCertPEM(certPEM)
		if err != nil {
			return nil, err
		}
		caKey, err := parseECDSAKeyPEM(keyPEM)
		if err != nil {
			return nil, err
		}
		return &Store{Dir: dir, caCert: caCert, caKey: caKey, caCertPEM: certPEM, leafCert: map[string]*tlsCert{}}, nil
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := randSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "burpui Local CA",
			Organization: []string{"burpui"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}

	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	return &Store{Dir: dir, caCert: caCert, caKey: caKey, caCertPEM: certPEM, leafCert: map[string]*tlsCert{}}, nil
}

func (s *Store) RootCertPEM() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.caCertPEM...)
}

func (s *Store) LeafCert(host string) (certPEM []byte, keyPEM []byte, err error) {
	name := strings.TrimSpace(host)
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return nil, nil, fmt.Errorf("host vazio")
	}

	s.leafMu.Lock()
	if cached, ok := s.leafCert[name]; ok {
		certPEM = append([]byte(nil), cached.certPEM...)
		keyPEM = append([]byte(nil), cached.keyPEM...)
		s.leafMu.Unlock()
		return certPEM, keyPEM, nil
	}
	s.leafMu.Unlock()

	s.mu.Lock()
	caCert := s.caCert
	caKey := s.caKey
	s.mu.Unlock()

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serial, err := randSerial()
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	leaf := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: name,
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(0, 0, 7),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	if ip := net.ParseIP(name); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{name}
	}

	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	s.leafMu.Lock()
	if s.leafCert == nil {
		s.leafCert = map[string]*tlsCert{}
	}
	s.leafCert[name] = &tlsCert{certPEM: certPEM, keyPEM: keyPEM}
	s.leafMu.Unlock()

	return append([]byte(nil), certPEM...), append([]byte(nil), keyPEM...), nil
}

func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func parseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(pemBytes)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("cert pem inválido")
	}
	return x509.ParseCertificate(b.Bytes)
}

func parseECDSAKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	b, _ := pem.Decode(pemBytes)
	if b == nil {
		return nil, fmt.Errorf("key pem inválido")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key não é ECDSA")
	}
	return key, nil
}
