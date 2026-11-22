package ca

import (
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func verify(t *testing.T, a *Authority, host string) {
	t.Helper()
	c, err := a.Leaf(host)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Leaf.Verify(x509.VerifyOptions{Roots: a.Pool(), DNSName: host, CurrentTime: a.now()}); err != nil {
		t.Fatalf("verify %s: %v", host, err)
	}
}

func TestLeafMinting(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"example.test", "127.0.0.1", "::1", "EXAMPLE.test."} {
		verify(t, a, h)
	}
	c1, _ := a.Leaf("example.test")
	c2, _ := a.Leaf("example.test")
	if c1 != c2 {
		t.Fatal("leaf not cached")
	}
	if _, err := a.Leaf(""); err == nil {
		t.Fatal("empty host accepted")
	}
}

func TestLeafRenewsNearExpiry(t *testing.T) {
	a, _ := Generate()
	c1, _ := a.Leaf("a.test")
	a.now = func() time.Time { return time.Now().Add(leafLifetime - time.Minute) }
	c2, _ := a.Leaf("a.test")
	if c1 == c2 {
		t.Fatal("expiring leaf reused")
	}
}

func TestLRUEvicts(t *testing.T) {
	l := newLRU[int](2)
	l.put("a", 1)
	l.put("b", 2)
	l.get("a")
	l.put("c", 3)
	if _, ok := l.get("b"); ok || l.len() != 2 {
		t.Fatal("expected b evicted")
	}
}

func TestPersistAndPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ca")
	a, created, err := LoadOrCreate(dir)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	info, _ := os.Stat(filepath.Join(dir, keyFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %o", info.Mode().Perm())
	}
	b, created, err := LoadOrCreate(dir)
	if err != nil || created || b.Fingerprint() != a.Fingerprint() {
		t.Fatal("reload mismatch", err, created)
	}
	verify(t, b, "x.test")
	if err := os.Chmod(filepath.Join(dir, keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.Is(err, ErrInsecurePerms) {
		t.Fatalf("want ErrInsecurePerms, got %v", err)
	}
}
