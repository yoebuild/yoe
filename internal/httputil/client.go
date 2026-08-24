// Package httputil holds the HTTP client yoe uses to download things from
// the network: source archives, prebuilt packages, distro feed indexes, and
// yoe's own release binaries.
//
// Everything here fetches whole files as opaque bytes, so the client is
// configured for that one job rather than for general web traffic. Two
// settings differ from Go's default, and both matter for correctness or
// reliability rather than for speed.
//
// # HTTP/1.1 only
//
// Downloading one large file per request gains nothing from HTTP/2
// multiplexing, and sometimes transfers more slowly under it. More
// importantly, some CDN endpoints reset the stream instead of serving the
// file: kernel.org has answered PROTOCOL_ERROR on stream 1 for every attempt
// from an affected network path, while the same URL over HTTP/1.1 downloads
// normally. Retrying cannot recover from that, since each attempt opens a
// fresh connection and negotiates the same broken protocol, so the transport
// avoids the protocol rather than the callers working around it.
//
// This also matches how other build systems fetch sources. BitBake and
// Buildroot both shell out to wget, which speaks only HTTP/1.1, so neither
// has ever negotiated HTTP/2 for a download.
//
// # No transparent decompression
//
// Go's default transport advertises `Accept-Encoding: gzip` and then inflates
// the response before the caller sees it. That corrupts every download yoe
// makes, because yoe wants the bytes the server was asked for:
//
//   - Savannah and some of its mirrors (nongnu.askapache.com does, and
//     mirrors.ocf.berkeley.edu does not, with savannah's redirector choosing
//     between them per request) serve a `.tar.gz` with `Content-Encoding:
//     gzip`. A decoded body leaves a bare tar on disk under a `.tar.gz` name,
//     and extraction later picks gzip by extension and fails with "gzip:
//     invalid header".
//   - Checksums are the more important case: a decoded body hashes to
//     something other than what upstream published, so a unit's sha256 would
//     match on one mirror and fail on the next.
//   - Feed indexes are verified by signature over the compressed bytes, and
//     decompressed explicitly afterward.
//
// Nothing is lost by it. Archives, packages, and feed indexes are already
// compressed, so gzip transfer encoding would not shrink them.
package httputil

import "net/http"

// Client downloads files over HTTP/1.1 without transparent decompression.
// It is safe for concurrent use, and callers should share it rather than
// constructing their own so that connection reuse and the settings above
// apply to every download yoe makes.
var Client = &http.Client{Transport: NewTransport()}

// NewTransport returns a transport configured the way Client's is. Use it
// when a caller needs its own client — a distinct timeout, say — and still
// wants yoe's download settings.
func NewTransport() *http.Transport {
	// Left unset, a transport negotiates HTTP/2 over TLS by default.
	var protocols http.Protocols
	protocols.SetHTTP1(true)

	return &http.Transport{
		DisableCompression: true,
		Protocols:          &protocols,
	}
}
