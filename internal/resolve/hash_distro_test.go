package resolve

import (
	"testing"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestUnitHash_DistroGating_Neutral(t *testing.T) {
	u := &yoestar.Unit{Name: "openssl", Version: "3.0.0", Class: "unit"}
	withEmpty := UnitHash(u, "x86_64", nil, "", "")
	withSame := UnitHash(u, "x86_64", nil, "", "")
	if withEmpty != withSame {
		t.Errorf("empty effectiveDistro should produce stable hash; %q vs %q", withEmpty, withSame)
	}
}

func TestUnitHash_DistroGating_Differentiates(t *testing.T) {
	u := &yoestar.Unit{Name: "openssl", Version: "3.0.0", Class: "unit"}
	alpineHash := UnitHash(u, "x86_64", nil, "", "alpine")
	debianHash := UnitHash(u, "x86_64", nil, "", "debian")
	if alpineHash == debianHash {
		t.Errorf("alpine and debian effectiveDistros should produce different hashes")
	}
}

func TestUnitHash_DistroGating_EmptyVsAlpine(t *testing.T) {
	// Stays cache-neutral: a unit hashed without effective_distro stays
	// the same as today; once a walker supplies "alpine" the hash flips.
	// That's the documented one-time invalidation.
	u := &yoestar.Unit{Name: "openssl", Version: "3.0.0", Class: "unit"}
	pre := UnitHash(u, "x86_64", nil, "", "")
	post := UnitHash(u, "x86_64", nil, "", "alpine")
	if pre == post {
		t.Errorf("hash with effective_distro should differ from hash without")
	}
}
