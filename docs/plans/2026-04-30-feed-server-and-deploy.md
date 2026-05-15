# Feed server and `yoe deploy` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the three-command stack from
[2026-04-30-feed-server-and-deploy-design.md](../specs/2026-04-30-feed-server-and-deploy-design.md):
`yoe serve` (long-lived HTTP feed + mDNS), `yoe device repo {add,remove,list}`
(configure the device's apk repo file), and `yoe deploy <unit> <host>` (build +
ephemeral feed + ssh + apk add, persistent repo file on the device).

**Architecture:** A new `internal/feed/` package owns the HTTP server and mDNS
advertisement. `internal/device/` (which already hosts `flash`) gains `repo.go`
and `deploy.go` for the ssh/scp shellouts and deploy orchestration. CLI wiring
lives in `cmd/yoe/serve.go`, `cmd/yoe/device.go`, `cmd/yoe/deploy.go`. ssh and
scp are shelled out to so user `~/.ssh/config` works; mDNS uses
`github.com/libp2p/zeroconf/v2`.

**Tech Stack:** Go 1.25, `net/http` stdlib, `github.com/libp2p/zeroconf/v2`,
shelled-out `ssh` / `scp`.

---

## Phase Overview

| Phase | Name                       | Deliverable                                                      |
| ----- | -------------------------- | ---------------------------------------------------------------- |
| 1     | `internal/feed/` primitive | `feed.Server` with HTTP + mDNS, unit-tested                      |
| 2     | `yoe serve` CLI            | Long-lived feed runnable from a project tree                     |
| 3     | Device repo ops            | `internal/device/repo.go` — Add/Remove/List + key push           |
| 4     | `yoe device repo` CLI      | `add`, `remove`, `list` subcommands wired                        |
| 5     | Deploy orchestration       | `internal/device/deploy.go` — feed reuse, ssh+apk, teardown      |
| 6     | `yoe deploy` CLI           | One-shot build → feed → install verb                             |
| 7     | Docs                       | `docs/feed-server.md`, updates to existing docs, changelog entry |

Each phase is independently shippable. After phase 2 the static-host OTA flow
gains a one-command server. After phase 4 a developer can configure a device
manually. Phase 6 closes the loop.

---

## Phase 1: `internal/feed/` primitive

The feed primitive is what `yoe serve` and `yoe deploy` both compose. It wraps
an `http.Server` rooted at the project's `repo/` tree plus an mDNS advertisement
(optional).

**Files:**

- Create: `internal/feed/feed.go`
- Create: `internal/feed/server.go`
- Create: `internal/feed/mdns.go`
- Create: `internal/feed/feed_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/libp2p/zeroconf/v2`)

### Task 1.1: Add the mDNS dependency

- [ ] **Step 1: Run go get**

```bash
cd /scratch4/yoe/yoe-ng
go get github.com/libp2p/zeroconf/v2
go mod tidy
```

Expected: `go.mod` now lists `github.com/libp2p/zeroconf/v2`. `go.sum`
populated.

- [ ] **Step 2: Verify build still passes**

```bash
source envsetup.sh && yoe_build
```

Expected: clean build, no errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add libp2p/zeroconf/v2 for mDNS feed advertisement"
```

### Task 1.2: HTTP server (`internal/feed/server.go`)

The HTTP server is `http.FileServer` over the repo dir, with request logging.

- [ ] **Step 1: Write the failing test**

Create `internal/feed/feed_test.go`:

```go
package feed

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerServesRepoFiles(t *testing.T) {
	repoDir := t.TempDir()
	// Create a fake apk and index under <repoDir>/myproj/x86_64/
	archDir := filepath.Join(repoDir, "myproj", "x86_64")
	if err := os.MkdirAll(archDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archDir, "APKINDEX.tar.gz"), []byte("fake-index"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := StartHTTP(repoDir, "127.0.0.1:0", io.Discard)
	if err != nil {
		t.Fatalf("StartHTTP: %v", err)
	}
	defer srv.Stop()

	url := "http://" + srv.Addr() + "/myproj/x86_64/APKINDEX.tar.gz"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "fake-index") {
		t.Fatalf("body = %q, want to contain fake-index", body)
	}
}

func TestServerStop(t *testing.T) {
	repoDir := t.TempDir()
	srv, err := StartHTTP(repoDir, "127.0.0.1:0", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Second Stop should be a no-op, not panic.
	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/feed/ -run TestServer -v
```

Expected: build error or FAIL — package/types not defined.

- [ ] **Step 3: Implement `internal/feed/server.go`**

```go
// Package feed serves a yoe project's apk repository over HTTP and
// optionally advertises it on mDNS so devices and `yoe deploy` can
// discover it.
package feed

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// HTTPServer is the bare HTTP layer of a feed: an http.Server bound to
// a TCP listener, serving a directory tree.
type HTTPServer struct {
	listener net.Listener
	server   *http.Server

	stopOnce sync.Once
	stopErr  error
}

// StartHTTP listens on bindAddr and serves files rooted at repoDir.
// bindAddr is a host:port string; pass ":0" or "host:0" for an ephemeral
// port (read it back via Addr()). logW receives one line per request
// (method, path, status, bytes, duration); pass io.Discard to silence.
func StartHTTP(repoDir, bindAddr string, logW io.Writer) (*HTTPServer, error) {
	ln, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", bindAddr, err)
	}

	mux := http.NewServeMux()
	fs := http.FileServer(http.Dir(repoDir))
	mux.Handle("/", logHandler(fs, logW))

	s := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		_ = s.Serve(ln)
	}()

	return &HTTPServer{listener: ln, server: s}, nil
}

// Addr returns the actual listening address, e.g. "127.0.0.1:8765".
func (s *HTTPServer) Addr() string {
	return s.listener.Addr().String()
}

// Port returns just the port number for use when constructing URLs that
// pair the port with a different (mDNS) hostname.
func (s *HTTPServer) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Stop shuts the server down, draining in-flight requests for up to
// 5 seconds. Safe to call multiple times.
func (s *HTTPServer) Stop() error {
	s.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.stopErr = s.server.Shutdown(ctx)
	})
	return s.stopErr
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func logHandler(next http.Handler, w io.Writer) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: rw}
		next.ServeHTTP(lrw, r)
		fmt.Fprintf(w, "%s %s %d %d %s\n",
			r.Method, r.URL.Path, lrw.status, lrw.bytes, time.Since(start).Round(time.Millisecond))
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/feed/ -run TestServer -v
```

Expected: PASS for both TestServerServesRepoFiles and TestServerStop.

- [ ] **Step 5: Commit**

```bash
git add internal/feed/server.go internal/feed/feed_test.go
git commit -m "feat(feed): HTTP server primitive serving repo dir"
```

### Task 1.3: mDNS advertise + browse (`internal/feed/mdns.go`)

- [ ] **Step 1: Add the mDNS test**

Append to `internal/feed/feed_test.go`:

```go
func TestMDNSAdvertiseAndBrowse(t *testing.T) {
	if testing.Short() {
		t.Skip("mDNS test requires loopback multicast")
	}

	adv, err := AdvertiseMDNS(MDNSConfig{
		Instance: "yoe-test-feed",
		Project:  "test-project",
		Path:     "/test-project",
		Archs:    []string{"x86_64"},
		Port:     8765,
	})
	if err != nil {
		t.Fatalf("AdvertiseMDNS: %v", err)
	}
	defer adv.Stop()

	// Give the registration a moment to propagate.
	time.Sleep(200 * time.Millisecond)

	results, err := BrowseMDNS(2 * time.Second)
	if err != nil {
		t.Fatalf("BrowseMDNS: %v", err)
	}

	var found *MDNSResult
	for i, r := range results {
		if r.Instance == "yoe-test-feed" {
			found = &results[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("did not discover yoe-test-feed in %d results", len(results))
	}
	if found.Project != "test-project" {
		t.Errorf("Project = %q, want test-project", found.Project)
	}
	if found.Path != "/test-project" {
		t.Errorf("Path = %q, want /test-project", found.Path)
	}
	if found.Port != 8765 {
		t.Errorf("Port = %d, want 8765", found.Port)
	}
}
```

Add `"time"` to the test file's imports if not already present.

- [ ] **Step 2: Implement `internal/feed/mdns.go`**

```go
package feed

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

const mdnsServiceType = "_yoe-feed._tcp"

// MDNSConfig holds the parameters for an mDNS advertisement.
type MDNSConfig struct {
	Instance string   // e.g. "yoe-myproj"
	Project  string   // project name for TXT record
	Path     string   // URL path component, e.g. "/myproj"
	Archs    []string // archs available, joined with comma in TXT
	Port     int
}

// MDNSAdvertisement is a running zeroconf registration. Stop() to unregister.
type MDNSAdvertisement struct {
	server *zeroconf.Server
}

// AdvertiseMDNS registers a _yoe-feed._tcp service. The hostname used in
// SRV records comes from the OS; zeroconf picks the local IPs automatically.
func AdvertiseMDNS(cfg MDNSConfig) (*MDNSAdvertisement, error) {
	if cfg.Instance == "" {
		return nil, fmt.Errorf("mDNS instance name is empty")
	}
	if cfg.Port == 0 {
		return nil, fmt.Errorf("mDNS port is zero")
	}
	txt := []string{
		"project=" + cfg.Project,
		"path=" + cfg.Path,
		"arch=" + strings.Join(cfg.Archs, ","),
	}
	srv, err := zeroconf.Register(cfg.Instance, mdnsServiceType, "local.", cfg.Port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("zeroconf.Register: %w", err)
	}
	return &MDNSAdvertisement{server: srv}, nil
}

// Stop unregisters the advertisement.
func (a *MDNSAdvertisement) Stop() {
	if a == nil || a.server == nil {
		return
	}
	a.server.Shutdown()
}

// MDNSResult is a discovered _yoe-feed._tcp instance.
type MDNSResult struct {
	Instance string
	Host     string // .local hostname from SRV
	Port     int
	Project  string
	Path     string
	Archs    []string
	IPs      []net.IP // available addresses
}

// BrowseMDNS scans for _yoe-feed._tcp instances for up to timeout.
func BrowseMDNS(timeout time.Duration) ([]MDNSResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	entries := make(chan *zeroconf.ServiceEntry, 16)
	if err := zeroconf.Browse(ctx, mdnsServiceType, "local.", entries); err != nil {
		return nil, fmt.Errorf("zeroconf.Browse: %w", err)
	}

	var results []MDNSResult
	for e := range entries {
		r := MDNSResult{
			Instance: e.Instance,
			Host:     e.HostName,
			Port:     e.Port,
		}
		for _, ip := range e.AddrIPv4 {
			r.IPs = append(r.IPs, ip)
		}
		for _, kv := range e.Text {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			switch k {
			case "project":
				r.Project = v
			case "path":
				r.Path = v
			case "arch":
				if v != "" {
					r.Archs = strings.Split(v, ",")
				}
			}
		}
		results = append(results, r)
	}
	return results, nil
}

// URL constructs the http URL for this discovered feed.
func (r MDNSResult) URL() string {
	host := strings.TrimSuffix(r.Host, ".")
	return fmt.Sprintf("http://%s:%d%s", host, r.Port, r.Path)
}
```

- [ ] **Step 3: Run the mDNS test**

```bash
go test ./internal/feed/ -run TestMDNS -v
```

Expected: PASS. If the test machine has no multicast loopback (CI containers
often don't), the test may need `-short` skipping — already handled.

- [ ] **Step 4: Commit**

```bash
git add internal/feed/mdns.go internal/feed/feed_test.go
git commit -m "feat(feed): mDNS advertise and browse for _yoe-feed._tcp"
```

### Task 1.4: Combine into a single `feed.Server` (`internal/feed/feed.go`)

- [ ] **Step 1: Add the integration test**

Append to `internal/feed/feed_test.go`:

```go
func TestServerWithMDNS(t *testing.T) {
	if testing.Short() {
		t.Skip("requires loopback multicast")
	}
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "p", "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}

	srv, err := Start(Config{
		RepoDir:  repoDir,
		BindAddr: "127.0.0.1:0",
		Project:  "p",
		Archs:    []string{"x86_64"},
		Instance: "yoe-p-test",
		LogW:     io.Discard,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if !strings.HasPrefix(srv.URL(), "http://") {
		t.Errorf("URL = %q, expected http:// prefix", srv.URL())
	}
}

func TestServerNoMDNS(t *testing.T) {
	repoDir := t.TempDir()
	srv, err := Start(Config{
		RepoDir:  repoDir,
		BindAddr: "127.0.0.1:0",
		Project:  "p",
		Archs:    []string{"x86_64"},
		Instance: "",
		NoMDNS:   true,
		LogW:     io.Discard,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if srv.URL() == "" {
		t.Error("URL is empty")
	}
}
```

- [ ] **Step 2: Implement `internal/feed/feed.go`**

```go
package feed

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Config configures a Server.
type Config struct {
	RepoDir  string   // root of the served file tree (project's repo/)
	BindAddr string   // e.g. "0.0.0.0:8765" or "127.0.0.1:0" for ephemeral
	Project  string   // project name (used for default URL path and TXT)
	Archs    []string // archs present under repo/<project>/
	Instance string   // mDNS instance name; default "yoe-<project>"
	NoMDNS   bool     // skip mDNS advertisement entirely
	HostName string   // override .local hostname (default: os.Hostname() + ".local")
	LogW     io.Writer
}

// Server is a feed: HTTP serving + optional mDNS advertisement.
type Server struct {
	http *HTTPServer
	mdns *MDNSAdvertisement
	cfg  Config
	host string
}

// Start brings up the HTTP server and (unless NoMDNS) registers an mDNS
// advertisement. Returns once both are ready.
func Start(cfg Config) (*Server, error) {
	if cfg.RepoDir == "" {
		return nil, fmt.Errorf("RepoDir is empty")
	}
	if cfg.Project == "" {
		return nil, fmt.Errorf("Project is empty")
	}
	if cfg.LogW == nil {
		cfg.LogW = io.Discard
	}
	if cfg.Instance == "" {
		cfg.Instance = "yoe-" + cfg.Project
	}

	host := cfg.HostName
	if host == "" {
		h, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("os.Hostname: %w", err)
		}
		host = strings.TrimSuffix(h, ".local") + ".local"
	}

	httpSrv, err := StartHTTP(cfg.RepoDir, cfg.BindAddr, cfg.LogW)
	if err != nil {
		return nil, err
	}

	s := &Server{http: httpSrv, cfg: cfg, host: host}

	if !cfg.NoMDNS {
		adv, err := AdvertiseMDNS(MDNSConfig{
			Instance: cfg.Instance,
			Project:  cfg.Project,
			Path:     "/" + cfg.Project,
			Archs:    cfg.Archs,
			Port:     httpSrv.Port(),
		})
		if err != nil {
			httpSrv.Stop()
			return nil, err
		}
		s.mdns = adv
	}

	return s, nil
}

// URL returns the user-facing feed URL using the .local hostname.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s:%d/%s", s.host, s.http.Port(), s.cfg.Project)
}

// Addr returns the bound address (host:port) for direct-IP access.
func (s *Server) Addr() string { return s.http.Addr() }

// Port returns the bound port.
func (s *Server) Port() int { return s.http.Port() }

// Host returns the .local hostname used in URLs.
func (s *Server) Host() string { return s.host }

// Stop tears down mDNS first (so no new client discovers a feed about
// to disappear), then drains HTTP.
func (s *Server) Stop() error {
	if s.mdns != nil {
		s.mdns.Stop()
	}
	return s.http.Stop()
}
```

- [ ] **Step 3: Run all feed tests**

```bash
go test ./internal/feed/ -v
```

Expected: PASS for all tests.

- [ ] **Step 4: Commit**

```bash
git add internal/feed/feed.go internal/feed/feed_test.go
git commit -m "feat(feed): combined HTTP+mDNS Server with single Start/Stop"
```

---

## Phase 2: `yoe serve` CLI

Wire `feed.Server` into a long-lived command.

**Files:**

- Create: `cmd/yoe/serve.go`
- Modify: `cmd/yoe/main.go` (dispatch + usage)

### Task 2.1: Implement `cmdServe`

- [ ] **Step 1: Create `cmd/yoe/serve.go`**

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/YoeDistro/yoe-ng/internal/feed"
	"github.com/YoeDistro/yoe-ng/internal/repo"
)

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8765, "TCP port to listen on")
	bind := fs.String("bind", "0.0.0.0", "listen address")
	noMDNS := fs.Bool("no-mdns", false, "skip mDNS advertisement")
	instance := fs.String("service-name", "", "mDNS instance name (default: yoe-<project>)")
	fs.Parse(args)

	proj := loadProject()
	if proj.Name == "" {
		fmt.Fprintf(os.Stderr, "Error: project has no name\n")
		os.Exit(1)
	}

	repoDir := filepath.Dir(repo.RepoDir(proj, projectDir()))

	archs, err := projectArchs(proj, projectDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	srv, err := feed.Start(feed.Config{
		RepoDir:  repoDir,
		BindAddr: fmt.Sprintf("%s:%d", *bind, *port),
		Project:  proj.Name,
		Archs:    archs,
		Instance: *instance,
		NoMDNS:   *noMDNS,
		LogW:     os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("serving %s/ at %s\n", repoDir, srv.URL())
	if !*noMDNS {
		instName := *instance
		if instName == "" {
			instName = "yoe-" + proj.Name
		}
		fmt.Printf("mDNS:  _yoe-feed._tcp.local. instance=%s\n", instName)
	}
	fmt.Println("press ctrl-c to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	fmt.Println("\nshutting down...")
	if err := srv.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		os.Exit(1)
	}
}

// projectArchs returns the arch subdirectories present under
// <projectDir>/repo/<project>/. Empty if the repo dir doesn't exist yet.
func projectArchs(proj projectLike, dir string) ([]string, error) {
	repoDir := repo.RepoDir(proj, dir)
	dirs, err := repo.ArchDirs(repoDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return dirs, nil
}

// projectLike narrows the bits projectArchs needs from yoestar.Project so
// the helper is testable without loading the whole project.
type projectLike interface {
	GetName() string
}
```

If the existing `yoestar.Project` doesn't already have a `GetName()` helper,
just take `*yoestar.Project` directly here — drop the interface. Check:

```bash
grep -n "func (.*Project) Name\|GetName" internal/starlark/*.go
```

If none, replace `projectLike` with `*yoestar.Project`. The interface is only
there to keep this file decoupled — fine to drop.

- [ ] **Step 2: Wire dispatch in `cmd/yoe/main.go`**

In the switch around line 60, add a case alphabetically near "run":

```go
	case "serve":
		cmdServe(cmdArgs)
```

In `printUsage()`, add a line near the existing `run` description:

```go
	fmt.Fprintf(os.Stderr, "  serve                   Run an HTTP+mDNS feed for the project's repo\n")
```

- [ ] **Step 3: Smoke test**

```bash
source envsetup.sh && yoe_build
cd testdata/e2e-project   # or any project dir
yoe serve --port 8765 --no-mdns &
SERVE_PID=$!
sleep 1
curl -sI http://127.0.0.1:8765/ | head -1
kill $SERVE_PID
```

Expected: `HTTP/1.1 200 OK` (or 404 if the repo dir is empty — both prove the
server is up). No panics.

- [ ] **Step 4: Commit**

```bash
git add cmd/yoe/serve.go cmd/yoe/main.go
git commit -m "feat(cli): yoe serve runs the feed primitive long-lived"
```

---

## Phase 3: Device repo ops (`internal/device/repo.go`)

ssh and scp shellouts for configuring `/etc/apk/repositories.d/<name>.list` on a
target device, plus a key push helper.

**Files:**

- Create: `internal/device/repo.go`
- Create: `internal/device/repo_test.go`
- Create: `internal/device/sshcmd.go` (small helper used by repo.go and later
  deploy.go)

### Task 3.1: ssh / scp shell-out helper (`sshcmd.go`)

- [ ] **Step 1: Implement `internal/device/sshcmd.go`**

```go
package device

import (
	"context"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// SSHTarget identifies a remote device for ssh/scp shellouts.
type SSHTarget struct {
	Host string // hostname, IP, or user@host
	User string // overrides any user@ prefix on Host; empty = none
	Port int    // 0 = default (22)
}

// argv returns the leading flags for ssh and scp invocations.
func (t SSHTarget) sshArgs() []string {
	var args []string
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	args = append(args, "-o", "BatchMode=no")
	return args
}

func (t SSHTarget) scpArgs() []string {
	var args []string
	if t.Port != 0 {
		args = append(args, "-P", strconv.Itoa(t.Port))
	}
	return args
}

// dest returns the user@host string (preferring the explicit User field).
func (t SSHTarget) dest() string {
	if t.User == "" {
		return t.Host
	}
	host := t.Host
	if i := strings.Index(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	return t.User + "@" + host
}

// SSHRunner shells out to `ssh` for remote command execution. The factory
// is exposed so tests can substitute a stub.
type SSHRunner func(ctx context.Context, target SSHTarget, remoteScript string, stdout, stderr io.Writer) error
type SCPRunner func(ctx context.Context, target SSHTarget, src, dst string, stdout, stderr io.Writer) error

// DefaultSSH runs ssh from $PATH.
func DefaultSSH(ctx context.Context, target SSHTarget, remoteScript string, stdout, stderr io.Writer) error {
	args := target.sshArgs()
	args = append(args, target.dest(), remoteScript)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// DefaultSCP runs scp from $PATH.
func DefaultSCP(ctx context.Context, target SSHTarget, src, dst string, stdout, stderr io.Writer) error {
	args := target.scpArgs()
	args = append(args, src, target.dest()+":"+dst)
	cmd := exec.CommandContext(ctx, "scp", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
```

- [ ] **Step 2: No tests yet for this file — covered by `repo_test.go` via
      stubbed runners.** Verify it builds:

```bash
go build ./internal/device/
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add internal/device/sshcmd.go
git commit -m "feat(device): ssh/scp shellout helpers for remote ops"
```

### Task 3.2: Repo ops + tests (`repo.go`, `repo_test.go`)

- [ ] **Step 1: Write the tests first**

Create `internal/device/repo_test.go`:

```go
package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type recordedSSH struct {
	target SSHTarget
	script string
}

func newSSHRecorder(out string, retErr error) (*[]recordedSSH, SSHRunner) {
	var recs []recordedSSH
	return &recs, func(_ context.Context, target SSHTarget, script string, stdout, stderr ioWriter) error {
		recs = append(recs, recordedSSH{target: target, script: script})
		fmt.Fprint(stdout, out)
		return retErr
	}
}

// ioWriter shim — io.Writer needs an import; keep this helper minimal in tests.
type ioWriter = interface {
	Write(p []byte) (n int, err error)
}

type recordedSCP struct {
	target SSHTarget
	src    string
	dst    string
}

func newSCPRecorder(retErr error) (*[]recordedSCP, SCPRunner) {
	var recs []recordedSCP
	return &recs, func(_ context.Context, target SSHTarget, src, dst string, _, _ ioWriter) error {
		recs = append(recs, recordedSCP{target: target, src: src, dst: dst})
		return retErr
	}
}

func TestRepoAddWritesFile(t *testing.T) {
	sshRecs, ssh := newSSHRecorder("OK\n", nil)
	_, scp := newSCPRecorder(nil)

	ops := RepoOps{SSH: ssh, SCP: scp}
	target := SSHTarget{Host: "dev-pi.local", User: "root"}
	err := ops.Add(context.Background(), target, RepoAddInput{
		Name:    "yoe-dev",
		FeedURL: "http://laptop.local:8765/myproj",
		Out:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(*sshRecs) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(*sshRecs))
	}
	got := (*sshRecs)[0].script
	want := []string{
		"mkdir -p /etc/apk/repositories.d",
		"http://laptop.local:8765/myproj",
		"/etc/apk/repositories.d/yoe-dev.list",
		"apk update",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("ssh script missing %q\n--- script ---\n%s", w, got)
		}
	}
}

func TestRepoAddPushesKey(t *testing.T) {
	_, ssh := newSSHRecorder("OK\n", nil)
	scpRecs, scp := newSCPRecorder(nil)
	ops := RepoOps{SSH: ssh, SCP: scp}
	target := SSHTarget{Host: "dev-pi.local"}
	err := ops.Add(context.Background(), target, RepoAddInput{
		Name:        "yoe-dev",
		FeedURL:     "http://laptop.local:8765/myproj",
		PushKeyFrom: "/keys/myproj.rsa.pub",
		PushKeyTo:   "/etc/apk/keys/myproj.rsa.pub",
		Out:         &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(*scpRecs) != 1 {
		t.Fatalf("expected 1 scp call, got %d", len(*scpRecs))
	}
	rec := (*scpRecs)[0]
	if rec.src != "/keys/myproj.rsa.pub" || rec.dst != "/etc/apk/keys/myproj.rsa.pub" {
		t.Errorf("scp src=%q dst=%q, want /keys/myproj.rsa.pub -> /etc/apk/keys/myproj.rsa.pub", rec.src, rec.dst)
	}
}

func TestRepoRemove(t *testing.T) {
	sshRecs, ssh := newSSHRecorder("", nil)
	ops := RepoOps{SSH: ssh}
	err := ops.Remove(context.Background(), SSHTarget{Host: "dev-pi"}, "yoe-dev", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(*sshRecs) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(*sshRecs))
	}
	if !strings.Contains((*sshRecs)[0].script, "rm -f /etc/apk/repositories.d/yoe-dev.list") {
		t.Errorf("script: %s", (*sshRecs)[0].script)
	}
}

func TestRepoListPropagatesError(t *testing.T) {
	_, ssh := newSSHRecorder("", errors.New("connection refused"))
	ops := RepoOps{SSH: ssh}
	err := ops.List(context.Background(), SSHTarget{Host: "dev-pi"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error")
	}
}
```

The `ioWriter` shim avoids an `io` import in the test recorder; replace later if
you'd rather just `import "io"`.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/device/ -run TestRepo -v
```

Expected: build error — `RepoOps`, `RepoAddInput` not defined.

- [ ] **Step 3: Implement `internal/device/repo.go`**

```go
package device

import (
	"context"
	"fmt"
	"io"
)

// RepoOps groups Add/Remove/List against a remote target. SSH and SCP are
// pluggable so tests can substitute stubs; production wiring uses
// DefaultSSH and DefaultSCP.
type RepoOps struct {
	SSH SSHRunner
	SCP SCPRunner
}

// RepoAddInput carries the parameters for Add.
type RepoAddInput struct {
	Name        string // basename for /etc/apk/repositories.d/<name>.list
	FeedURL     string
	PushKeyFrom string    // local path; empty = skip
	PushKeyTo   string    // remote path
	Out         io.Writer // streams ssh stdout/stderr
}

// Add writes the repo file on the target and runs apk update.
func (r RepoOps) Add(ctx context.Context, t SSHTarget, in RepoAddInput) error {
	if in.Name == "" {
		return fmt.Errorf("repo name is empty")
	}
	if in.FeedURL == "" {
		return fmt.Errorf("feed URL is empty")
	}

	if in.PushKeyFrom != "" {
		if r.SCP == nil {
			return fmt.Errorf("SCP runner is nil but key push requested")
		}
		if err := r.SCP(ctx, t, in.PushKeyFrom, in.PushKeyTo, in.Out, in.Out); err != nil {
			return fmt.Errorf("scp key %s -> %s: %w", in.PushKeyFrom, in.PushKeyTo, err)
		}
	}

	script := fmt.Sprintf(`set -e
mkdir -p /etc/apk/repositories.d
printf '%%s\n' '%s' > /etc/apk/repositories.d/%s.list
apk update
`, in.FeedURL, in.Name)

	if r.SSH == nil {
		return fmt.Errorf("SSH runner is nil")
	}
	return r.SSH(ctx, t, script, in.Out, in.Out)
}

// Remove deletes /etc/apk/repositories.d/<name>.list on the target.
func (r RepoOps) Remove(ctx context.Context, t SSHTarget, name string, out io.Writer) error {
	if name == "" {
		return fmt.Errorf("repo name is empty")
	}
	script := fmt.Sprintf("rm -f /etc/apk/repositories.d/%s.list\n", name)
	return r.SSH(ctx, t, script, out, out)
}

// List cats /etc/apk/repositories and the .list files in repositories.d/.
func (r RepoOps) List(ctx context.Context, t SSHTarget, stdout, stderr io.Writer) error {
	script := `set -e
for f in /etc/apk/repositories /etc/apk/repositories.d/*.list; do
    [ -e "$f" ] || continue
    while IFS= read -r line; do
        printf '%s: %s\n' "$f" "$line"
    done < "$f"
done
`
	return r.SSH(ctx, t, script, stdout, stderr)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/device/ -run TestRepo -v
```

Expected: PASS for all four tests.

- [ ] **Step 5: Commit**

```bash
git add internal/device/repo.go internal/device/repo_test.go
git commit -m "feat(device): repo Add/Remove/List ops via ssh+scp shellouts"
```

---

## Phase 4: `yoe device repo` CLI

**Files:**

- Create: `cmd/yoe/device.go`
- Modify: `cmd/yoe/main.go` (dispatch + usage)

### Task 4.1: Implement `cmdDevice` dispatch

- [ ] **Step 1: Create `cmd/yoe/device.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/YoeDistro/yoe-ng/internal/artifact"
	"github.com/YoeDistro/yoe-ng/internal/device"
	"github.com/YoeDistro/yoe-ng/internal/feed"
	"github.com/YoeDistro/yoe-ng/internal/repo"
)

func cmdDevice(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s device <repo> ...\n", os.Args[0])
		os.Exit(1)
	}
	switch args[0] {
	case "repo":
		cmdDeviceRepo(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown device subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdDeviceRepo(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s device repo <add|remove|list> ...\n", os.Args[0])
		os.Exit(1)
	}
	switch args[0] {
	case "add":
		cmdDeviceRepoAdd(args[1:])
	case "remove":
		cmdDeviceRepoRemove(args[1:])
	case "list":
		cmdDeviceRepoList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown device repo subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func cmdDeviceRepoAdd(args []string) {
	fs := flag.NewFlagSet("device repo add", flag.ExitOnError)
	feedURL := fs.String("feed", "", "explicit feed URL")
	name := fs.String("name", "yoe-dev", "repo file basename")
	pushKey := fs.Bool("push-key", false, "copy project pubkey to /etc/apk/keys/")
	user := fs.String("user", "root", "ssh user")
	sshPort := fs.Int("ssh-port", 22, "ssh port")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s device repo add <target> [--feed URL] [--name N] [--push-key] [--user U] [--ssh-port P]\n", os.Args[0])
		os.Exit(1)
	}
	target := device.SSHTarget{Host: fs.Arg(0), User: *user, Port: *sshPort}

	url := *feedURL
	if url == "" {
		discovered, err := discoverFeed()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		url = discovered
	}

	in := device.RepoAddInput{Name: *name, FeedURL: url, Out: os.Stdout}

	if *pushKey {
		proj := loadProject()
		signer, err := artifact.LoadOrGenerateSigner(proj.Name, proj.SigningKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: load signer: %v\n", err)
			os.Exit(1)
		}
		repoDir := repo.RepoDir(proj, projectDir())
		in.PushKeyFrom = filepath.Join(repo.KeysDir(repoDir), signer.KeyName)
		in.PushKeyTo = "/etc/apk/keys/" + signer.KeyName
	}

	ops := device.RepoOps{SSH: device.DefaultSSH, SCP: device.DefaultSCP}
	if err := ops.Add(context.Background(), target, in); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("configured %s -> %s on %s\n", *name, url, target.Host)
}

func cmdDeviceRepoRemove(args []string) {
	fs := flag.NewFlagSet("device repo remove", flag.ExitOnError)
	name := fs.String("name", "yoe-dev", "repo file basename")
	user := fs.String("user", "root", "ssh user")
	sshPort := fs.Int("ssh-port", 22, "ssh port")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s device repo remove <target> [--name N]\n", os.Args[0])
		os.Exit(1)
	}
	target := device.SSHTarget{Host: fs.Arg(0), User: *user, Port: *sshPort}
	ops := device.RepoOps{SSH: device.DefaultSSH}
	if err := ops.Remove(context.Background(), target, *name, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func cmdDeviceRepoList(args []string) {
	fs := flag.NewFlagSet("device repo list", flag.ExitOnError)
	user := fs.String("user", "root", "ssh user")
	sshPort := fs.Int("ssh-port", 22, "ssh port")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s device repo list <target>\n", os.Args[0])
		os.Exit(1)
	}
	target := device.SSHTarget{Host: fs.Arg(0), User: *user, Port: *sshPort}
	ops := device.RepoOps{SSH: device.DefaultSSH}
	if err := ops.List(context.Background(), target, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// discoverFeed returns the URL of the unique _yoe-feed._tcp instance on the
// LAN that matches the current project. Errors if 0 or >1 results match.
func discoverFeed() (string, error) {
	results, err := feed.BrowseMDNS(1 * time.Second)
	if err != nil {
		return "", fmt.Errorf("mDNS browse: %w", err)
	}

	// If we're inside a project, filter to that project's feed.
	var projectName string
	if proj, _ := tryLoadProject(); proj != nil {
		projectName = proj.Name
	}

	var matches []feed.MDNSResult
	for _, r := range results {
		if projectName != "" && r.Project != projectName {
			continue
		}
		matches = append(matches, r)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no yoe feed discovered on the LAN — pass --feed URL")
	case 1:
		return matches[0].URL(), nil
	default:
		var hint []string
		for _, m := range matches {
			hint = append(hint, fmt.Sprintf("  %s -> %s", m.Instance, m.URL()))
		}
		return "", fmt.Errorf("found %d feeds:\n%s\npass --feed to disambiguate",
			len(matches), strings.Join(hint, "\n"))
	}
}
```

`tryLoadProject` is a helper that returns `(nil, err)` instead of os.Exit when
no project is found. Add it to `cmd/yoe/main.go` near `loadProject`:

```go
func tryLoadProject() (*yoestar.Project, error) {
	defer func() { _ = recover() }()
	return loadProjectErr("")
}
```

If `loadProjectErr` doesn't exist yet, the simpler implementation is: recover
the panic from `loadProject` and return nil. Easier still: just shell out to a
check that the project file exists, and skip the project-name filter if not.
Adapt to whatever pattern the codebase uses. The intent is "best-effort project
name for feed filtering."

Add `"strings"` to the import list of `device.go`.

- [ ] **Step 2: Wire dispatch in `cmd/yoe/main.go`**

In the switch around line 60:

```go
	case "device":
		cmdDevice(cmdArgs)
```

In `printUsage()`:

```go
	fmt.Fprintf(os.Stderr, "  device repo             Manage apk repos on a target device (add, remove, list)\n")
```

- [ ] **Step 3: Build and smoke test**

```bash
yoe_build
yoe device repo list dev-pi.local --user root  # against a real device, optional
```

Expected: clean build. `device repo list` errors cleanly if no device.

- [ ] **Step 4: Commit**

```bash
git add cmd/yoe/device.go cmd/yoe/main.go
git commit -m "feat(cli): yoe device repo {add,remove,list}"
```

---

## Phase 5: Deploy orchestration (`internal/device/deploy.go`)

The `Deploy()` entry point: build (assumed already done by caller for
testability), reuse-or-start a feed, ssh to the target, run apk add, tear down
ephemeral feed.

**Files:**

- Create: `internal/device/deploy.go`
- Create: `internal/device/deploy_test.go`

### Task 5.1: Implement `Deploy()`

- [ ] **Step 1: Write the test**

Create `internal/device/deploy_test.go`:

```go
package device

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeployScriptInstallsUnit(t *testing.T) {
	sshRecs, ssh := newSSHRecorder("", nil)
	out := &bytes.Buffer{}
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi", User: "root"},
		Unit:    "myapp",
		FeedURL: "http://laptop.local:8765/myproj",
		Out:     out,
		SSH:     ssh,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(*sshRecs) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(*sshRecs))
	}
	script := (*sshRecs)[0].script
	for _, want := range []string{
		"mkdir -p /etc/apk/repositories.d",
		"http://laptop.local:8765/myproj",
		"/etc/apk/repositories.d/yoe-dev.list",
		"apk update",
		"apk add --upgrade myapp",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
}

func TestDeployPropagatesAPKError(t *testing.T) {
	_, ssh := newSSHRecorder("", errors.New("apk: package not found"))
	err := Deploy(context.Background(), DeployInput{
		Target:  SSHTarget{Host: "dev-pi"},
		Unit:    "ghost",
		FeedURL: "http://laptop.local:8765/p",
		Out:     &bytes.Buffer{},
		SSH:     ssh,
	})
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Implement `internal/device/deploy.go`**

```go
package device

import (
	"context"
	"fmt"
	"io"
)

// DeployInput parameterizes Deploy.
//
// Deploy itself is the post-build orchestration step: pick/start a feed,
// ssh to the target, run apk add. Building <unit> is the caller's job —
// the CLI runs `yoe build` ahead of this and starts/stops the feed.
type DeployInput struct {
	Target  SSHTarget
	Unit    string
	FeedURL string // already resolved (mDNS reuse or ephemeral)
	Out     io.Writer
	SSH     SSHRunner // defaults to DefaultSSH if nil
}

// Deploy writes /etc/apk/repositories.d/yoe-dev.list with FeedURL and
// runs `apk add --upgrade <Unit>` on the target. The repo file is left
// in place — that's the persistent feed config the spec requires.
func Deploy(ctx context.Context, in DeployInput) error {
	if in.Unit == "" {
		return fmt.Errorf("unit is empty")
	}
	if in.FeedURL == "" {
		return fmt.Errorf("feed URL is empty")
	}
	ssh := in.SSH
	if ssh == nil {
		ssh = DefaultSSH
	}
	if in.Out == nil {
		in.Out = io.Discard
	}

	script := fmt.Sprintf(`set -e
mkdir -p /etc/apk/repositories.d
printf '%%s\n' '%s' > /etc/apk/repositories.d/yoe-dev.list
apk update
apk add --upgrade %s
`, in.FeedURL, in.Unit)

	return ssh(ctx, in.Target, script, in.Out, in.Out)
}
```

- [ ] **Step 3: Run the deploy tests**

```bash
go test ./internal/device/ -run TestDeploy -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/device/deploy.go internal/device/deploy_test.go
git commit -m "feat(device): Deploy orchestrates ssh + apk add against a feed URL"
```

---

## Phase 6: `yoe deploy` CLI

Wires together the build pipeline, feed resolution, ephemeral feed (if needed),
and `device.Deploy()`.

**Files:**

- Create: `cmd/yoe/deploy.go`
- Modify: `cmd/yoe/main.go` (dispatch + usage)

### Task 6.1: Implement `cmdDeploy`

- [ ] **Step 1: Create `cmd/yoe/deploy.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/YoeDistro/yoe-ng/internal/device"
	"github.com/YoeDistro/yoe-ng/internal/feed"
	"github.com/YoeDistro/yoe-ng/internal/repo"
	yoestar "github.com/YoeDistro/yoe-ng/internal/starlark"
)

func cmdDeploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	user := fs.String("user", "root", "ssh user")
	sshPort := fs.Int("ssh-port", 22, "ssh port")
	port := fs.Int("port", 8765, "feed port")
	hostIP := fs.String("host-ip", "", "advertise this IP instead of <hostname>.local")
	fs.Parse(args)
	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s deploy <unit> <host> [flags]\n", os.Args[0])
		os.Exit(1)
	}
	unitName := fs.Arg(0)
	hostArg := fs.Arg(1)

	proj := loadProject()
	unit, ok := proj.Units[unitName]
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: unit %q not found\n", unitName)
		os.Exit(1)
	}
	if unit.Class == "image" {
		fmt.Fprintf(os.Stderr, "Error: image targets are flashed, not deployed; use `yoe flash %s`\n", unitName)
		os.Exit(1)
	}

	// 1. Build (reuse the existing build path).
	if err := buildUnit(proj, unitName); err != nil {
		fmt.Fprintf(os.Stderr, "Error: build %s: %v\n", unitName, err)
		os.Exit(1)
	}

	// 2. Resolve a feed URL: existing yoe serve, or start ephemeral.
	feedURL, stopFeed, err := resolveOrStartFeed(proj, projectDir(), *port, *hostIP)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: feed: %v\n", err)
		os.Exit(1)
	}
	defer stopFeed()

	target := device.SSHTarget{Host: hostArg, User: *user, Port: *sshPort}
	err = device.Deploy(context.Background(), device.DeployInput{
		Target:  target,
		Unit:    unitName,
		FeedURL: feedURL,
		Out:     os.Stdout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("deployed %s to %s (feed: %s)\n", unitName, hostArg, feedURL)
}

// buildUnit invokes the same code path `yoe build <unit>` uses. Replace
// this with the actual exported helper your build.go uses; the search:
//
//   grep -n "func.*Build\b" internal/build/*.go cmd/yoe/main.go
func buildUnit(proj *yoestar.Project, unit string) error {
	return runBuildCommand([]string{unit})
}

// runBuildCommand re-uses cmdBuild() but returns instead of exiting.
// Implement by extracting the body of cmdBuild into a helper that
// returns an error, then have cmdBuild call os.Exit on err.
func runBuildCommand(args []string) error {
	// TODO during implementation: refactor cmdBuild into cmdBuild +
	// runBuild(args) (error). Easiest: copy cmdBuild's body here for the
	// minimal subset (unit name only), or add a new exported function in
	// cmd/yoe/main.go. Both work; refactor is cleaner.
	cmdBuild(args)
	return nil
}

// resolveOrStartFeed returns a feed URL and a teardown func. If a yoe
// serve advertising this project is already on the LAN, reuse it (teardown
// is a no-op). Otherwise spin up an ephemeral feed.
func resolveOrStartFeed(proj *yoestar.Project, projDir string, port int, hostIP string) (string, func(), error) {
	results, _ := feed.BrowseMDNS(500 * time.Millisecond)
	for _, r := range results {
		if r.Project == proj.Name {
			return r.URL(), func() {}, nil
		}
	}

	repoDir := filepath.Dir(repo.RepoDir(proj, projDir))
	archs, _ := repo.ArchDirs(repo.RepoDir(proj, projDir))

	bind := "0.0.0.0"
	hostName := ""
	if hostIP != "" {
		bind = hostIP
		hostName = hostIP
	}
	srv, err := feed.Start(feed.Config{
		RepoDir:  repoDir,
		BindAddr: net.JoinHostPort(bind, strconv.Itoa(port)),
		Project:  proj.Name,
		Archs:    archs,
		NoMDNS:   true, // ephemeral; do not advertise
		HostName: hostName,
		LogW:     os.Stderr,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start ephemeral feed: %w", err)
	}
	return strings.TrimSuffix(srv.URL(), "/"), srv.Stop, nil
}
```

The `buildUnit` glue is the only fragile bit — extract a refactor-friendly form
of `cmdBuild` during implementation. If that's too invasive, shell out to the
same binary recursively:

```go
func buildUnit(_ *yoestar.Project, unit string) error {
	cmd := exec.Command(os.Args[0], "build", unit)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

Either is fine; pick whichever has the smaller diff.

- [ ] **Step 2: Wire dispatch in `cmd/yoe/main.go`**

In the switch:

```go
	case "deploy":
		cmdDeploy(cmdArgs)
```

In `printUsage()`:

```go
	fmt.Fprintf(os.Stderr, "  deploy <unit> <host>    Build and install a unit on a running yoe device\n")
```

- [ ] **Step 3: Smoke test**

```bash
yoe_build
# In a project dir, against a real device:
yoe deploy bash dev-pi.local
```

Expected: build runs (no-op if cached), feed starts (or reuses serve), device
installs `bash`, deploy prints success.

- [ ] **Step 4: Commit**

```bash
git add cmd/yoe/deploy.go cmd/yoe/main.go
git commit -m "feat(cli): yoe deploy <unit> <host> closes the dev loop"
```

---

## Phase 7: Documentation and changelog

**Files:**

- Create: `docs/feed-server.md`
- Modify: `docs/on-device-apk.md`, `docs/dev-env.md`, `docs/roadmap.md`,
  `CHANGELOG.md`

### Task 7.1: User guide (`docs/feed-server.md`)

- [ ] **Step 1: Create the doc**

Write a user-focused walkthrough covering the three commands. Use the spec's
"Examples" sections as starting material; expand with prose for the typical
workflows:

1. One-time setup against a fresh device (`yoe device repo add --push-key`).
2. Long-running serve + repeated `apk add` from the device.
3. One-shot deploy of a single unit.

About 150–250 lines. Reference `docs/on-device-apk.md` for trust model context.

- [ ] **Step 2: Update `docs/on-device-apk.md`**

In the section that describes `/etc/apk/repositories` (around the "Pointing at a
repository" subsection), add a short subsection:

> ### Pointing at a yoe-served feed
>
> For development, run `yoe serve` on your build host and configure the device
> with `yoe device repo add <host>`. See [feed-server.md](feed-server.md).

- [ ] **Step 3: Update `docs/dev-env.md`**

Replace the "Fast deploy" section's "scp + apk add" prose with the actual
pull-based semantics, linking to `feed-server.md`.

- [ ] **Step 4: Update `docs/roadmap.md`**

Move the "Fast deploy" and "Feed server" lines from the Next/Developer
Experience sections to a new "Done" section (or remove them with a reference to
`feed-server.md`).

- [ ] **Step 5: Update `CHANGELOG.md`**

Add an entry under `[Unreleased]`:

```markdown
- **Pull-based dev loop.** `yoe deploy <unit> <host>` builds and installs a unit
  on a running yoe device with full apk dependency resolution. Pair with
  `yoe serve` and `yoe device repo add` to keep a device pointed at your dev
  feed for ad-hoc `apk add` from the device. See
  [docs/feed-server.md](docs/feed-server.md).
```

- [ ] **Step 6: Run formatting check**

```bash
yoe_format
yoe_format_check
```

Expected: no failures.

- [ ] **Step 7: Update plan + spec INDEX**

In `docs/superpowers/plans/INDEX.md`, add:

```
| Feed server and deploy (2026-04-30)    | Done        | yoe serve, yoe deploy, yoe device repo {add,remove,list} |
```

In `docs/superpowers/specs/INDEX.md`, change the spec status to `Done`.

- [ ] **Step 8: Commit**

```bash
git add docs/ CHANGELOG.md
git commit -m "docs: feed server, yoe deploy, yoe device repo user guide and changelog"
```

---

## Verification

Final sanity checks before marking the plan complete:

- [ ] `go test ./...` passes.
- [ ] `yoe_build` succeeds.
- [ ] `yoe serve --no-mdns` starts and serves index/apks via `curl`.
- [ ] `yoe device repo list <real-device>` lists the on-device repo files
      (manual, against a Pi or QEMU vm).
- [ ] `yoe deploy bash <real-device>` installs bash and the device's
      `/etc/apk/repositories.d/yoe-dev.list` is written.
- [ ] After `yoe deploy`, the device can independently run `apk add htop`
      against the dev feed (when serve or another deploy is running).
- [ ] `yoe_format_check` clean.

## Out-of-band debt to track

- The `buildUnit` helper in `cmd/yoe/deploy.go` either refactors `cmdBuild` or
  shells out. If shelled out, leave a TODO to refactor once another command
  needs the same path (deploy is the second).
- mDNS testing on CI: if CI uses containers without multicast, mDNS tests must
  use `testing.Short` and CI must pass `-short`. Verify.
