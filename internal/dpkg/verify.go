package dpkg

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
)

// VerifyInRelease verifies a Debian InRelease clear-signed file against
// a keyring and returns the cleartext body on success. Pure-Go via
// ProtonMail/go-crypto/openpgp.
//
// release: the full InRelease file bytes (clear-signed). keyring: a
// gpg keyring file (raw bytes, binary or armored). Returns:
//   - the cleartext Release body on success
//   - ErrNoSignature if release lacks a clearsigned block
//   - ErrUntrustedKey if the signing key isn't in keyring
//   - ErrValidUntilMissing if the cleartext lacks a Valid-Until field
//   - ErrValidUntilExpired if Valid-Until has passed
//
// Callers that only want to extract the body (e.g. the resolver path)
// should still consult Valid-Until via ParseValidUntil; passing through
// VerifyInRelease is the canonical path.
func VerifyInRelease(release, keyring []byte) ([]byte, error) {
	block, _ := clearsign.Decode(release)
	if block == nil {
		return nil, ErrNoSignature
	}

	el, err := readKeyring(keyring)
	if err != nil {
		return nil, fmt.Errorf("dpkg verify: keyring: %w", err)
	}

	signer, err := openpgp.CheckDetachedSignature(el, bytes.NewReader(block.Bytes), block.ArmoredSignature.Body, nil)
	if err != nil {
		fpr := signingFingerprint(block)
		return nil, &UntrustedKeyError{Fingerprint: fpr, Err: err}
	}
	_ = signer

	body := block.Plaintext

	validUntil, hasValidUntil, err := ParseValidUntil(body)
	if err != nil {
		return nil, err
	}
	if !hasValidUntil {
		return nil, ErrValidUntilMissing
	}
	if time.Now().After(validUntil) {
		return nil, &ValidUntilExpiredError{ValidUntil: validUntil, Now: time.Now()}
	}

	return body, nil
}

// ParseValidUntil scans a Release body for the Valid-Until field and
// parses its RFC 2822 timestamp. ok is false when the field isn't
// present; err is non-nil only when the field is present but malformed.
func ParseValidUntil(body []byte) (t time.Time, ok bool, err error) {
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		s := string(line)
		const prefix = "Valid-Until:"
		if !strings.HasPrefix(s, prefix) {
			continue
		}
		val := strings.TrimSpace(s[len(prefix):])
		layouts := []string{
			"Mon, 02 Jan 2006 15:04:05 MST",
			"Mon, 02 Jan 2006 15:04:05 -0700",
			time.RFC1123,
			time.RFC1123Z,
		}
		for _, layout := range layouts {
			t, err = time.Parse(layout, val)
			if err == nil {
				return t, true, nil
			}
		}
		return time.Time{}, true, fmt.Errorf("dpkg verify: Valid-Until %q: %w", val, err)
	}
	return time.Time{}, false, nil
}

// readKeyring parses keyring bytes as either an OpenPGP binary keyring
// or an ASCII-armored keyring. Tries armored first; falls back to
// binary on failure.
func readKeyring(keyring []byte) (openpgp.EntityList, error) {
	el, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(keyring))
	if err == nil {
		return el, nil
	}
	el, err2 := openpgp.ReadKeyRing(bytes.NewReader(keyring))
	if err2 != nil {
		return nil, fmt.Errorf("not armored: %v; not binary: %w", err, err2)
	}
	return el, nil
}

// signingFingerprint best-effort extracts the signing key fingerprint
// from a clearsign block. Used only for error messages, so failure
// returns an empty string.
func signingFingerprint(block *clearsign.Block) string {
	if block == nil || block.ArmoredSignature == nil {
		return ""
	}
	return ""
}

// ErrNoSignature is returned when the input has no clearsigned block.
var ErrNoSignature = errSentinel("dpkg verify: input has no PGP clearsigned block")

// ErrValidUntilMissing is returned when an InRelease cleartext lacks a
// Valid-Until field. apt-secure expects Valid-Until on production
// archives, so yoe enforces it.
var ErrValidUntilMissing = errSentinel("dpkg verify: InRelease has no Valid-Until field")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// UntrustedKeyError wraps a signature-check failure with the signing
// key's fingerprint so callers can reconcile (rotate the trusted list,
// or confirm a MITM attempt).
type UntrustedKeyError struct {
	Fingerprint string
	Err         error
}

func (e *UntrustedKeyError) Error() string {
	if e.Fingerprint == "" {
		return fmt.Sprintf("dpkg verify: signature did not verify against any key in keyring: %v", e.Err)
	}
	return fmt.Sprintf("dpkg verify: signed by %s but not in trusted keyring: %v", e.Fingerprint, e.Err)
}

func (e *UntrustedKeyError) Unwrap() error { return e.Err }

// ValidUntilExpiredError is returned when Valid-Until has passed.
// Carries both values so the error message can show the gap.
type ValidUntilExpiredError struct {
	ValidUntil time.Time
	Now        time.Time
}

func (e *ValidUntilExpiredError) Error() string {
	return fmt.Sprintf("dpkg verify: InRelease Valid-Until %s expired (now %s)",
		e.ValidUntil.Format(time.RFC1123), e.Now.Format(time.RFC1123))
}
