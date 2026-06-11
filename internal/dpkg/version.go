package dpkg

import (
	"fmt"

	"pault.ag/go/debian/version"
)

// Version is a parsed dpkg version (epoch, upstream, revision).
// Re-exports pault.ag/go/debian/version.Version under a yoe-friendly name.
type Version = version.Version

// ParseVersion parses a dpkg version string like "1:1.2.3-4". Returns
// an error for genuinely malformed input.
func ParseVersion(s string) (Version, error) {
	v, err := version.Parse(s)
	if err != nil {
		return Version{}, fmt.Errorf("dpkg: parse version %q: %w", s, err)
	}
	return v, nil
}

// CompareVersions returns -1 if a < b, 0 if equal, +1 if a > b under
// the dpkg version comparison algorithm. Tilde sorts before nothing
// ("1.0~rc1" < "1.0"); epoch dominates the upstream version
// ("1:1.0" > "99.0").
func CompareVersions(a, b string) (int, error) {
	av, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return version.Compare(av, bv), nil
}
