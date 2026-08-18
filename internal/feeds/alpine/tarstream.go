package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

// extractAPKINDEXFromStream looks for an APKINDEX entry inside a
// single gzip stream's tar payload. Returns (nil, nil) when this
// stream doesn't carry the index; caller walks subsequent streams.
//
// Used by update-feeds to decompose APKINDEX.tar.gz into its
// human-readable index file. Mirrors apkindex.ParseIndexTarGz's
// behavior but returns the bytes rather than the parsed entries —
// we want to write the index to disk verbatim, not normalize it.
func extractAPKINDEXFromStream(streamBytes []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(streamBytes))
	if err != nil {
		return nil, fmt.Errorf("gzip open: %w", err)
	}
	defer gz.Close()
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("tar entry: %w", err)
		}
		if hdr.Name != "APKINDEX" {
			continue
		}
		return io.ReadAll(tr)
	}
}
