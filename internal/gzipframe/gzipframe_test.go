package gzipframe

import (
	"bytes"
	"compress/gzip"
	"testing"
)

// gz returns body compressed as a single gzip stream, optionally with a
// filename header so the FNAME framing path is exercised.
func gz(t *testing.T, body, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Name = name
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// An apk is signature + control + data concatenated; the boundaries have
// to come back as the exact byte ranges of each, since callers checksum
// those ranges rather than the decompressed contents.
func TestBoundaries_ConcatenatedStreams(t *testing.T) {
	sig := gz(t, "signature", ".SIGN.RSA.key.pub")
	ctl := gz(t, "control segment", ".PKGINFO")
	dat := gz(t, "data segment", "")

	var file []byte
	file = append(file, sig...)
	file = append(file, ctl...)
	file = append(file, dat...)

	bounds, err := Boundaries(file)
	if err != nil {
		t.Fatalf("Boundaries: %v", err)
	}
	if len(bounds) != 3 {
		t.Fatalf("got %d bounds, want 3", len(bounds))
	}
	for i, want := range [][]byte{sig, ctl, dat} {
		if got := bounds[i].Bytes(file); !bytes.Equal(got, want) {
			t.Errorf("stream %d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
	// Ranges must be contiguous and cover the whole file.
	if bounds[0].Start != 0 || bounds[2].End != len(file) {
		t.Errorf("bounds %v do not span the file (%d bytes)", bounds, len(file))
	}
}

func TestStream(t *testing.T) {
	ctl := gz(t, "control segment", ".PKGINFO")
	file := append(gz(t, "signature", ""), ctl...)

	got, err := Stream(file, 1)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !bytes.Equal(got, ctl) {
		t.Errorf("stream 1 mismatch")
	}

	if _, err := Stream(file, 5); err == nil {
		t.Error("expected an error asking for a stream past the end")
	}
}

// Trailing bytes that don't start a gzip header end the scan rather than
// failing it — a caller passing a whole file gets the streams at its front.
func TestBoundaries_StopsAtNonGzip(t *testing.T) {
	file := append(gz(t, "hello", ""), []byte("not gzip at all")...)
	bounds, err := Boundaries(file)
	if err != nil {
		t.Fatalf("Boundaries: %v", err)
	}
	if len(bounds) != 1 {
		t.Fatalf("got %d bounds, want 1", len(bounds))
	}
}

// A stream that starts but doesn't finish must error: every offset past
// the damage is untrustworthy.
func TestBoundaries_Truncated(t *testing.T) {
	full := gz(t, "some reasonably long body to compress", "")
	if _, err := Boundaries(full[:len(full)-4]); err == nil {
		t.Error("expected an error on a truncated stream")
	}
}

func TestBoundaries_Empty(t *testing.T) {
	bounds, err := Boundaries(nil)
	if err != nil {
		t.Fatalf("Boundaries(nil): %v", err)
	}
	if len(bounds) != 0 {
		t.Errorf("got %d bounds, want 0", len(bounds))
	}
}
