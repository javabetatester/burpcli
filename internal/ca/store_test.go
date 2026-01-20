package ca

import "testing"

func TestRootThumbprintSHA1_Format(t *testing.T) {
	st, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	thumb := st.RootThumbprintSHA1()
	if len(thumb) != 40 {
		t.Fatalf("expected len 40, got %d (%q)", len(thumb), thumb)
	}
	for _, r := range thumb {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F') {
			continue
		}
		t.Fatalf("invalid char %q in %q", r, thumb)
	}
}
