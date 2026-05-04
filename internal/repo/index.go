package repo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yoebuild/yoe/internal/artifact"
)

// GenerateIndex scans repoDir for .apk files and produces an
// APKINDEX.tar.gz that apk(8) can use for dependency resolution. When
// signer is non-nil the index is signed (a signature stream is prepended)
// so apk update accepts it without --allow-untrusted.
func GenerateIndex(repoDir string, signer *artifact.Signer) error {
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return fmt.Errorf("reading repo dir: %w", err)
	}

	var apks []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".apk") {
			apks = append(apks, e.Name())
		}
	}
	sort.Strings(apks)

	if len(apks) == 0 {
		return nil // nothing to index
	}

	// Build APKINDEX content
	var buf strings.Builder
	for i, name := range apks {
		apkPath := filepath.Join(repoDir, name)

		info, err := os.Stat(apkPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", name, err)
		}

		hash, err := sha1base64(apkPath)
		if err != nil {
			return fmt.Errorf("hashing %s: %w", name, err)
		}

		pkginfo := extractPKGINFO(apkPath)
		// pkgname/pkgver/arch come from PKGINFO authoritatively. A missing
		// PKGINFO means the apk is malformed; we still emit an entry with
		// empty fields so the rest of the index isn't lost, but apk-tools
		// will reject it.
		pkgName := pkginfo.Get("pkgname")
		version := pkginfo.Get("pkgver")
		scope := pkginfo.Get("arch")
		installedSize := pkginfoSize(pkginfo)
		description := pkginfo.Get("pkgdesc")
		license := pkginfo.Get("license")
		buildDate := pkginfo.Get("builddate")
		origin := pkginfo.Get("origin")
		commit := pkginfo.Get("commit")
		url := pkginfo.Get("url")
		depends := strings.Join(pkginfo.values("depend"), " ")
		provides := strings.Join(pkginfo.values("provides"), " ")
		replaces := strings.Join(pkginfo.values("replaces"), " ")

		// Field order matches Alpine's apk index for diff sanity. apk-tools
		// is order-tolerant but matching keeps comparisons readable.
		fmt.Fprintf(&buf, "C:Q1%s\n", hash)
		fmt.Fprintf(&buf, "P:%s\n", pkgName)
		fmt.Fprintf(&buf, "V:%s\n", version)
		fmt.Fprintf(&buf, "A:%s\n", scope)
		fmt.Fprintf(&buf, "S:%d\n", info.Size())
		fmt.Fprintf(&buf, "I:%d\n", installedSize)
		fmt.Fprintf(&buf, "T:%s\n", description)
		if url != "" {
			fmt.Fprintf(&buf, "U:%s\n", url)
		}
		if license != "" {
			fmt.Fprintf(&buf, "L:%s\n", license)
		}
		if origin != "" {
			fmt.Fprintf(&buf, "o:%s\n", origin)
		}
		if buildDate != "" {
			fmt.Fprintf(&buf, "t:%s\n", buildDate)
		}
		if commit != "" {
			fmt.Fprintf(&buf, "c:%s\n", commit)
		}
		if depends != "" {
			fmt.Fprintf(&buf, "D:%s\n", depends)
		}
		if provides != "" {
			fmt.Fprintf(&buf, "p:%s\n", provides)
		}
		if replaces != "" {
			fmt.Fprintf(&buf, "r:%s\n", replaces)
		}
		if i < len(apks)-1 {
			buf.WriteString("\n")
		}
	}

	// Build APKINDEX.tar.gz into a buffer so we can sign it before writing.
	var indexBuf bytes.Buffer
	gw := gzip.NewWriter(&indexBuf)
	tw := tar.NewWriter(gw)

	content := []byte(buf.String())
	hdr := &tar.Header{
		Name:    "APKINDEX",
		Size:    int64(len(content)),
		Mode:    0644,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("writing tar content: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing index tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("closing index gzip: %w", err)
	}

	// Write signature + index. apk-tools 2.x reads APKINDEX.tar.gz as a
	// concatenated gzip stream: optional .SIGN.RSA.* signature, then the
	// actual index. Without signing, just the index stream.
	indexPath := filepath.Join(repoDir, "APKINDEX.tar.gz")
	f, err := os.Create(indexPath)
	if err != nil {
		return fmt.Errorf("creating APKINDEX.tar.gz: %w", err)
	}
	defer f.Close()

	if signer != nil {
		sigGz, err := signer.SignStream(indexBuf.Bytes())
		if err != nil {
			return fmt.Errorf("signing APKINDEX: %w", err)
		}
		if _, err := f.Write(sigGz); err != nil {
			return fmt.Errorf("writing index signature: %w", err)
		}
	}
	if _, err := f.Write(indexBuf.Bytes()); err != nil {
		return fmt.Errorf("writing APKINDEX body: %w", err)
	}

	return nil
}

// sha1base64 returns the base64-encoded SHA1 of an apk's control stream —
// what apk-tools puts in APKINDEX's `C:` line as the package "identity".
// The control stream is the FIRST gzip stream for unsigned apks, but the
// SECOND stream for signed apks (where the first is the .SIGN.RSA.* block).
// We detect the signed case by peeking inside the first stream; if its tar
// payload starts with a `.SIGN.` entry we skip ahead to the next stream
// before hashing.
//
// We feed the gzip decoder one byte at a time so its internal buffering
// can't read past the stream boundary — that gives us an exact byte count
// per stream, which we re-read raw and feed through SHA-1.
func sha1base64(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read the first stream and inspect its tar contents. If it's a
	// signature stream, advance past it so we hash the next (control)
	// stream instead.
	streamStart := int64(0)
	consumed, isSig, err := streamLenAndIsSig(f, streamStart)
	if err != nil {
		return "", err
	}
	if isSig {
		streamStart += consumed
		if _, err := f.Seek(streamStart, 0); err != nil {
			return "", err
		}
		consumed, _, err = streamLenAndIsSig(f, streamStart)
		if err != nil {
			return "", err
		}
	}

	// Re-open and hash exactly `consumed` bytes starting at streamStart.
	f2, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f2.Close()
	if _, err := f2.Seek(streamStart, 0); err != nil {
		return "", err
	}

	h := sha1.New()
	if _, err := io.CopyN(h, f2, consumed); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// streamLenAndIsSig reads exactly one gzip stream starting at the current
// file position and returns (compressed-bytes-consumed, isSignatureStream).
// Detects signature streams by checking whether the first tar entry's name
// starts with `.SIGN.` (apk-tools's convention).
func streamLenAndIsSig(f *os.File, _ int64) (int64, bool, error) {
	cr := &countingReader{r: &oneByteReader{r: f}}
	gr, err := gzip.NewReader(cr)
	if err != nil {
		return 0, false, err
	}
	gr.Multistream(false)

	tr := tar.NewReader(gr)
	hdr, terr := tr.Next()
	isSig := terr == nil && hdr != nil && strings.HasPrefix(hdr.Name, ".SIGN.")

	if _, err := io.Copy(io.Discard, gr); err != nil {
		return 0, false, err
	}
	if err := gr.Close(); err != nil {
		return 0, false, err
	}
	return cr.n, isSig, nil
}

// countingReader counts bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// oneByteReader caps every Read to a single byte so wrappers that buffer
// internally (gzip.Reader does) cannot pull ahead past the stream boundary.
// The cost (one syscall per byte) only matters in this function and the
// streams are tiny (~hundreds of bytes), so it's negligible.
type oneByteReader struct{ r io.Reader }

func (o *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

// pkginfoMap holds parsed PKGINFO key/value pairs. PKGINFO can have
// multiple lines with the same key (e.g., several `depend = X` lines), so
// each value slot is itself a slice. Single-valued keys use the first
// value via map[string] indexing for convenience.
type pkginfoMap map[string][]string

// values returns all values for a key (e.g., all `depend = X` entries).
func (p pkginfoMap) values(key string) []string { return p[key] }

// indexed access returns the first value (or "") for single-valued keys.
func (p pkginfoMap) Get(key string) string {
	v := p[key]
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

// extractPKGINFO opens an .apk and parses .PKGINFO into a key→[]value map.
// Returns an empty map on any failure (callers that need a specific field
// fall back to "" / 0 naturally).
func extractPKGINFO(apkPath string) pkginfoMap {
	out := pkginfoMap{}
	f, err := os.Open(apkPath)
	if err != nil {
		return out
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return out
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return out
		}
		if hdr.Name == ".PKGINFO" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return out
			}
			for _, line := range strings.Split(string(data), "\n") {
				idx := strings.Index(line, " = ")
				if idx < 0 {
					continue
				}
				key := line[:idx]
				val := line[idx+3:]
				out[key] = append(out[key], val)
			}
			return out
		}
	}
}

// pkginfoSize parses the "size" PKGINFO field as int64.
func pkginfoSize(p pkginfoMap) int64 {
	val := p.Get("size")
	if val == "" {
		return 0
	}
	var n int64
	fmt.Sscanf(val, "%d", &n)
	return n
}

