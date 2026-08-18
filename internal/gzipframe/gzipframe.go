// Package gzipframe parses gzip stream framing at the byte level.
//
// An apk file is three gzip streams concatenated end to end — signature,
// control, data — and an APKINDEX.tar.gz is two. Both formats identify a
// segment by its byte range in the original file rather than by its
// decompressed contents: an APKINDEX `C:` checksum is the sha1 of the
// control stream's raw bytes, and signature verification runs over the
// bytes following the signature stream. compress/gzip stops at the first
// stream's end without reporting where that was, so the boundaries have
// to be recovered by walking the framing directly.
//
// The decode-and-discard here is what makes the offsets exact: the length
// of a deflate stream is not recorded anywhere in the header, so the only
// way to find where it ends is to run it. That cost is why callers should
// hold onto the boundaries rather than recompute them.
package gzipframe

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"io"
)

// Bound is the half-open byte range [Start, End) of one gzip stream
// within a larger buffer, including that stream's header and its
// CRC32 + ISIZE trailer.
type Bound struct {
	Start int
	End   int
}

// Bytes returns the slice of data this bound covers.
func (b Bound) Bytes(data []byte) []byte { return data[b.Start:b.End] }

// Boundaries scans data for concatenated gzip streams and returns the
// byte range of each. It stops cleanly at the first position that does
// not begin a gzip header, so trailing non-gzip bytes end the scan
// rather than erroring — which is what lets a caller pass a whole file
// and get back only the streams at its front.
//
// A stream that starts but does not complete is an error: a truncated
// header, a corrupt deflate body, or a missing trailer all mean the
// caller cannot trust any offset past that point.
func Boundaries(data []byte) ([]Bound, error) {
	var out []Bound
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
				return nil, fmt.Errorf("gzipframe: truncated FEXTRA")
			}
			xlen := int(binary.LittleEndian.Uint16(data[hdrEnd : hdrEnd+2]))
			hdrEnd += 2 + xlen
		}
		if flg&0x08 != 0 { // FNAME — null-terminated
			for hdrEnd < len(data) && data[hdrEnd] != 0 {
				hdrEnd++
			}
			hdrEnd++
		}
		if flg&0x10 != 0 { // FCOMMENT — null-terminated
			for hdrEnd < len(data) && data[hdrEnd] != 0 {
				hdrEnd++
			}
			hdrEnd++
		}
		if flg&0x02 != 0 { // FHCRC
			hdrEnd += 2
		}
		if hdrEnd > len(data) {
			return nil, fmt.Errorf("gzipframe: truncated gzip header")
		}

		// Run the deflate stream to find where it ends. br.Len() is
		// what's left unread, so the difference is what was consumed.
		br := bytes.NewReader(data[hdrEnd:])
		zr := flate.NewReader(br)
		if _, err := io.Copy(io.Discard, zr); err != nil {
			zr.Close()
			return nil, fmt.Errorf("gzipframe: deflate stream %d: %w", len(out), err)
		}
		if err := zr.Close(); err != nil {
			return nil, fmt.Errorf("gzipframe: deflate close stream %d: %w", len(out), err)
		}
		deflateConsumed := (len(data) - hdrEnd) - br.Len()
		end := hdrEnd + deflateConsumed + 8 // +8 for CRC32 + ISIZE trailer
		if end > len(data) {
			return nil, fmt.Errorf("gzipframe: truncated gzip trailer")
		}
		out = append(out, Bound{Start: start, End: end})
		pos = end
	}
	return out, nil
}

// Streams splits data into its gzip streams and requires that they
// account for every byte. Use it when the whole buffer is supposed to be
// gzip and nothing else — an .apk, an APKINDEX.tar.gz — so a file that
// is not actually in that format is reported rather than silently
// yielding however many streams happened to parse before the garbage.
func Streams(data []byte) ([][]byte, error) {
	bounds, err := Boundaries(data)
	if err != nil {
		return nil, err
	}
	consumed := 0
	if len(bounds) > 0 {
		consumed = bounds[len(bounds)-1].End
	}
	if consumed != len(data) {
		return nil, fmt.Errorf("gzipframe: %d trailing byte(s) after the last gzip stream at offset %d",
			len(data)-consumed, consumed)
	}
	out := make([][]byte, len(bounds))
	for i, b := range bounds {
		out[i] = b.Bytes(data)
	}
	return out, nil
}

// Stream returns the bytes of the n'th gzip stream in data (0-indexed).
// Returns an error naming what was found when data holds fewer streams,
// which is the common failure when a file turns out not to be the format
// the caller expected.
func Stream(data []byte, n int) ([]byte, error) {
	bounds, err := Boundaries(data)
	if err != nil {
		return nil, err
	}
	if n >= len(bounds) {
		return nil, fmt.Errorf("gzipframe: wanted stream %d, file has %d gzip stream(s)", n, len(bounds))
	}
	return bounds[n].Bytes(data), nil
}
