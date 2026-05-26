package apkindex

import (
	"archive/tar"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// VerifySignature verifies the RSA-SHA1 signature on an Alpine
// APKINDEX.tar.gz against a caller-supplied list of trusted public
// keys. Returns nil iff the file's signature is valid AND the signing
// key's filename matches one of trustedKeys.
//
// Pure-Go implementation — never consults the system keyring at
// /etc/apk/keys/. The whole point of this verifier is to enforce the
// trust list a maintainer declared via `alpine_feed(keys=[...])`;
// shelling out to apk-tools would bypass that list.
//
// Trust matching is by filename: each trustedKeys entry is a path to
// an Alpine-style public key (e.g.,
// keys/alpine-devel@lists.alpinelinux.org-6165ee59.rsa.pub). The
// matching key has the same basename as the suffix on the tarball's
// `.SIGN.RSA.<key>` entry. A signed tarball whose key doesn't match
// any trusted-list entry — even if the signature would otherwise
// verify — is rejected.
//
// Failure modes produce distinctive errors callers can pattern-match:
//
//   - ErrNoSignature: tarball has no .SIGN.RSA.* entry
//   - ErrUntrustedKey: signing key not in trustedKeys
//   - ErrSignatureMismatch: RSA verification failed
//
// I/O errors and malformed tarballs surface as wrapped errors.
func VerifySignature(tarballPath string, trustedKeys []string) error {
	data, err := os.ReadFile(tarballPath)
	if err != nil {
		return fmt.Errorf("apkindex verify: read %s: %w", tarballPath, err)
	}
	return VerifySignatureBytes(data, trustedKeys)
}

// VerifySignatureBytes is the in-memory variant of VerifySignature.
// Convenient for tests that synthesize a tarball without touching the
// filesystem, and for the `yoe update-feeds` path that has the bytes
// in hand from the HTTP download anyway.
func VerifySignatureBytes(data []byte, trustedKeys []string) error {
	bounds, err := gzipStreamBoundaries(data)
	if err != nil {
		return fmt.Errorf("apkindex verify: %w", err)
	}
	if len(bounds) < 2 {
		// One stream means no detached signature followed by content.
		// Alpine indices are always at least two streams when signed.
		return ErrNoSignature
	}

	// First stream carries .SIGN.RSA.<keyname> — extract.
	keyName, signature, err := readSignatureEntry(data[bounds[0][0]:bounds[0][1]])
	if err != nil {
		return fmt.Errorf("apkindex verify: %w", err)
	}
	if keyName == "" {
		return ErrNoSignature
	}

	// Locate the trusted key whose filename matches the .SIGN.RSA.<key>
	// suffix. The match is exact-basename so two different keys with
	// the same upstream maintainer prefix don't accidentally collide.
	var matched string
	for _, candidate := range trustedKeys {
		if filepath.Base(candidate) == keyName {
			matched = candidate
			break
		}
	}
	if matched == "" {
		return &UntrustedKeyError{KeyName: keyName, Trusted: trustedKeys}
	}

	pub, err := loadPublicKey(matched)
	if err != nil {
		return fmt.Errorf("apkindex verify: load key %s: %w", matched, err)
	}

	// Signed content is the raw bytes of every gzip stream after the
	// signature stream (data + description + index together).
	signedStart := bounds[0][1]
	digest := sha1.Sum(data[signedStart:])
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA1, digest[:], signature); err != nil {
		return &SignatureMismatchError{KeyName: keyName, Err: err}
	}
	return nil
}

// ErrNoSignature is returned when the tarball has no .SIGN.RSA.*
// entry. APKINDEX files that ship without a signature are out of
// scope for this verifier — the maintainer should fetch from a
// signed mirror.
var ErrNoSignature = errSentinel("apkindex verify: no .SIGN.RSA.* entry in tarball")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// UntrustedKeyError surfaces the key fingerprint the tarball was
// signed with and the trust list the caller provided, so the user can
// reconcile (rotate the trusted list to add a new Alpine key, or
// confirm a MITM attempt).
type UntrustedKeyError struct {
	KeyName string
	Trusted []string
}

func (e *UntrustedKeyError) Error() string {
	names := make([]string, len(e.Trusted))
	for i, t := range e.Trusted {
		names[i] = filepath.Base(t)
	}
	if len(names) == 0 {
		return fmt.Sprintf("apkindex verify: tarball signed by %q but trusted-key list is empty", e.KeyName)
	}
	return fmt.Sprintf("apkindex verify: tarball signed by %q which is not in trusted-key list %v", e.KeyName, names)
}

// SignatureMismatchError wraps the underlying rsa.VerifyPKCS1v15
// failure with the signing key name for human-readable diagnostics.
type SignatureMismatchError struct {
	KeyName string
	Err     error
}

func (e *SignatureMismatchError) Error() string {
	return fmt.Sprintf("apkindex verify: signature mismatch (key %q): %v", e.KeyName, e.Err)
}

func (e *SignatureMismatchError) Unwrap() error { return e.Err }

// readSignatureEntry walks the (already-isolated) first gzip stream
// and extracts the .SIGN.RSA.<keyname> entry. Returns the key name
// (the suffix after .SIGN.RSA.) and the entry's raw bytes (the
// signature itself). When the stream contains no signature entry,
// returns "", nil, nil — caller treats this as ErrNoSignature.
func readSignatureEntry(streamBytes []byte) (string, []byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(streamBytes))
	if err != nil {
		return "", nil, fmt.Errorf("gzip open signature stream: %w", err)
	}
	defer gz.Close()
	// Disable multistream; we only want this one signature stream.
	gz.Multistream(false)

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", nil, nil
		}
		if err != nil {
			return "", nil, fmt.Errorf("tar entry: %w", err)
		}
		const prefix = ".SIGN.RSA."
		if !strings.HasPrefix(hdr.Name, prefix) {
			continue
		}
		sig, err := io.ReadAll(tr)
		if err != nil {
			return "", nil, fmt.Errorf("read signature entry: %w", err)
		}
		return hdr.Name[len(prefix):], sig, nil
	}
}

// loadPublicKey reads a PEM-encoded RSA public key from path. Accepts
// SubjectPublicKeyInfo ("PUBLIC KEY") and PKCS#1 ("RSA PUBLIC KEY")
// envelopes — Alpine's keys are SubjectPublicKeyInfo but maintainers
// may import either.
func loadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("%s: not a PEM block", path)
	}
	switch block.Type {
	case "PUBLIC KEY":
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%s: not an RSA key (got %T)", path, k)
		}
		return rk, nil
	case "RSA PUBLIC KEY":
		return x509.ParsePKCS1PublicKey(block.Bytes)
	default:
		return nil, fmt.Errorf("%s: unsupported PEM type %q", path, block.Type)
	}
}

// gzipStreamBoundaries scans data for concatenated gzip streams and
// returns their start/end byte offsets. Duplicates the logic in
// internal/source/fetch.go to keep internal/apkindex free of cross-
// package deps for what's a self-contained byte-level operation. The
// two implementations should stay in sync; if a third caller appears,
// promote this to a shared internal/gzipframe package.
type gzipBound [2]int

func gzipStreamBoundaries(data []byte) ([]gzipBound, error) {
	var out []gzipBound
	pos := 0
	for pos < len(data) {
		if pos+10 > len(data) || data[pos] != 0x1f || data[pos+1] != 0x8b {
			break
		}
		start := pos
		flg := data[pos+3]
		hdrEnd := pos + 10
		if flg&0x04 != 0 { // FEXTRA
			if hdrEnd+2 > len(data) {
				return nil, fmt.Errorf("truncated FEXTRA")
			}
			xlen := int(binary.LittleEndian.Uint16(data[hdrEnd : hdrEnd+2]))
			hdrEnd += 2 + xlen
		}
		if flg&0x08 != 0 { // FNAME
			for hdrEnd < len(data) && data[hdrEnd] != 0 {
				hdrEnd++
			}
			hdrEnd++
		}
		if flg&0x10 != 0 { // FCOMMENT
			for hdrEnd < len(data) && data[hdrEnd] != 0 {
				hdrEnd++
			}
			hdrEnd++
		}
		if flg&0x02 != 0 { // FHCRC
			hdrEnd += 2
		}
		if hdrEnd > len(data) {
			return nil, fmt.Errorf("truncated gzip header")
		}
		br := bytes.NewReader(data[hdrEnd:])
		zr := flate.NewReader(br)
		if _, err := io.Copy(io.Discard, zr); err != nil {
			zr.Close()
			return nil, fmt.Errorf("deflate stream %d: %w", len(out), err)
		}
		if err := zr.Close(); err != nil {
			return nil, fmt.Errorf("deflate close stream %d: %w", len(out), err)
		}
		deflateConsumed := (len(data) - hdrEnd) - br.Len()
		end := hdrEnd + deflateConsumed + 8 // +8 for CRC32 + ISIZE trailer
		if end > len(data) {
			return nil, fmt.Errorf("truncated gzip trailer")
		}
		out = append(out, gzipBound{start, end})
		pos = end
	}
	return out, nil
}
