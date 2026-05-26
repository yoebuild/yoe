package apkindex

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// cacheWriter wraps a buffered writer with primitives for the entry
// encoding. Variable-length fields use uvarint length prefixes.
type cacheWriter struct {
	bw  *bufio.Writer
	buf [binary.MaxVarintLen64]byte
}

func newCacheWriter(w io.Writer) *cacheWriter {
	if bw, ok := w.(*bufio.Writer); ok {
		return &cacheWriter{bw: bw}
	}
	return &cacheWriter{bw: bufio.NewWriter(w)}
}

func (c *cacheWriter) flush() error {
	return c.bw.Flush()
}

func (c *cacheWriter) writeUvarint(v uint64) error {
	n := binary.PutUvarint(c.buf[:], v)
	_, err := c.bw.Write(c.buf[:n])
	return err
}

func (c *cacheWriter) writeVarint(v int64) error {
	n := binary.PutVarint(c.buf[:], v)
	_, err := c.bw.Write(c.buf[:n])
	return err
}

func (c *cacheWriter) writeString(s string) error {
	if err := c.writeUvarint(uint64(len(s))); err != nil {
		return err
	}
	_, err := c.bw.WriteString(s)
	return err
}

func (c *cacheWriter) writeBytes(b []byte) error {
	if err := c.writeUvarint(uint64(len(b))); err != nil {
		return err
	}
	_, err := c.bw.Write(b)
	return err
}

func (c *cacheWriter) writeStrings(ss []string) error {
	if err := c.writeUvarint(uint64(len(ss))); err != nil {
		return err
	}
	for _, s := range ss {
		if err := c.writeString(s); err != nil {
			return err
		}
	}
	return nil
}

// writeEntry encodes one Entry. Field order is fixed; adding fields
// requires bumping parserVersion in cache.go so older caches miss.
func (c *cacheWriter) writeEntry(e *Entry) error {
	if err := c.writeString(e.Name); err != nil {
		return err
	}
	if err := c.writeString(e.Version); err != nil {
		return err
	}
	if err := c.writeString(e.Description); err != nil {
		return err
	}
	if err := c.writeString(e.URL); err != nil {
		return err
	}
	if err := c.writeString(e.License); err != nil {
		return err
	}
	if err := c.writeString(e.Arch); err != nil {
		return err
	}
	if err := c.writeVarint(e.Size); err != nil {
		return err
	}
	if err := c.writeVarint(e.InstalledSize); err != nil {
		return err
	}
	if err := c.writeString(e.Origin); err != nil {
		return err
	}
	if err := c.writeString(e.Maintainer); err != nil {
		return err
	}
	if err := c.writeVarint(e.BuildTime); err != nil {
		return err
	}
	if err := c.writeString(e.Commit); err != nil {
		return err
	}
	if err := c.writeBytes(e.Checksum); err != nil {
		return err
	}
	if err := c.writeString(e.ChecksumText); err != nil {
		return err
	}
	if err := c.writeStrings(e.Deps); err != nil {
		return err
	}
	if err := c.writeStrings(e.Provides); err != nil {
		return err
	}
	if err := c.writeStrings(e.Replaces); err != nil {
		return err
	}
	if err := c.writeStrings(e.InstallIf); err != nil {
		return err
	}
	return nil
}

// cacheReader is the symmetric reader. EOF / unexpected EOF mid-entry
// returns ErrUnexpectedEOF so the cache loader can treat the whole file
// as corrupt and reparse fresh.
type cacheReader struct {
	br *bufio.Reader
}

func newCacheReader(r io.Reader) *cacheReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &cacheReader{br: br}
	}
	return &cacheReader{br: bufio.NewReader(r)}
}

func (c *cacheReader) readUvarint() (uint64, error) {
	return binary.ReadUvarint(c.br)
}

func (c *cacheReader) readVarint() (int64, error) {
	return binary.ReadVarint(c.br)
}

func (c *cacheReader) readString() (string, error) {
	n, err := c.readUvarint()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	// Guard against absurd lengths from corrupted files.
	if n > 1<<24 {
		return "", fmt.Errorf("apkindex cache: string length %d exceeds 16 MiB cap", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func (c *cacheReader) readBytes() ([]byte, error) {
	n, err := c.readUvarint()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	if n > 1<<16 {
		return nil, fmt.Errorf("apkindex cache: byte length %d exceeds 64 KiB cap", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *cacheReader) readStrings() ([]string, error) {
	n, err := c.readUvarint()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	if n > 1<<16 {
		return nil, fmt.Errorf("apkindex cache: string list length %d exceeds 64 K cap", n)
	}
	out := make([]string, n)
	for i := range n {
		s, err := c.readString()
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func (c *cacheReader) readEntry(e *Entry) error {
	var err error
	if e.Name, err = c.readString(); err != nil {
		return err
	}
	if e.Version, err = c.readString(); err != nil {
		return err
	}
	if e.Description, err = c.readString(); err != nil {
		return err
	}
	if e.URL, err = c.readString(); err != nil {
		return err
	}
	if e.License, err = c.readString(); err != nil {
		return err
	}
	if e.Arch, err = c.readString(); err != nil {
		return err
	}
	if e.Size, err = c.readVarint(); err != nil {
		return err
	}
	if e.InstalledSize, err = c.readVarint(); err != nil {
		return err
	}
	if e.Origin, err = c.readString(); err != nil {
		return err
	}
	if e.Maintainer, err = c.readString(); err != nil {
		return err
	}
	if e.BuildTime, err = c.readVarint(); err != nil {
		return err
	}
	if e.Commit, err = c.readString(); err != nil {
		return err
	}
	if e.Checksum, err = c.readBytes(); err != nil {
		return err
	}
	if e.ChecksumText, err = c.readString(); err != nil {
		return err
	}
	if e.Deps, err = c.readStrings(); err != nil {
		return err
	}
	if e.Provides, err = c.readStrings(); err != nil {
		return err
	}
	if e.Replaces, err = c.readStrings(); err != nil {
		return err
	}
	if e.InstallIf, err = c.readStrings(); err != nil {
		return err
	}
	return nil
}
