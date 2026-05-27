package dpkg

import "testing"

func TestCompareVersions_Tilde(t *testing.T) {
	got, err := CompareVersions("1.0~rc1", "1.0")
	if err != nil {
		t.Fatalf("CompareVersions: %v", err)
	}
	if got != -1 {
		t.Errorf("CompareVersions(1.0~rc1, 1.0): got %d, want -1", got)
	}
}

func TestCompareVersions_Epoch(t *testing.T) {
	got, err := CompareVersions("1:1.0", "99.0")
	if err != nil {
		t.Fatalf("CompareVersions: %v", err)
	}
	if got != 1 {
		t.Errorf("CompareVersions(1:1.0, 99.0): got %d, want +1", got)
	}
}

func TestCompareVersions_Equal(t *testing.T) {
	got, err := CompareVersions("2.36-9+deb12u9", "2.36-9+deb12u9")
	if err != nil {
		t.Fatalf("CompareVersions: %v", err)
	}
	if got != 0 {
		t.Errorf("CompareVersions equal: got %d, want 0", got)
	}
}
