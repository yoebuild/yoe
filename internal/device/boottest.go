package device

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	yoestar "github.com/yoebuild/yoe/internal/starlark"
	"golang.org/x/crypto/ssh"
)

// defaultBootTestTimeout bounds a boot test when the caller passes zero. A
// KVM-accelerated boot reaches the login prompt in well under a minute, but
// a host without /dev/kvm falls back to TCG emulation (several times
// slower), so the default is generous enough to cover an unaccelerated CI
// runner without hanging a stuck boot indefinitely.
const defaultBootTestTimeout = 5 * time.Minute

// bootLoginMarker is the substring the serial console prints once the system
// has reached userspace and started a getty. Both busybox/OpenRC (Alpine)
// and systemd (Debian) end boot with an agetty "<hostname> login:" prompt,
// so the bare "login:" suffix is a distro-agnostic "boot completed" signal.
const bootLoginMarker = "login:"

// sshHostPort returns the host-side port that forwards to guest port 22 for
// this machine + options, applying the same machine/CLI merge `yoe run`
// uses. The boot test SSHes to 127.0.0.1 on this port. It errors when no
// forward targets guest :22, since then there is no way to reach sshd.
func sshHostPort(machine *yoestar.Machine, opts QEMUOptions) (int, error) {
	for _, p := range mergeQEMUPorts(machine.QEMUPorts(), opts.Ports) {
		host, guest, ok := strings.Cut(p, ":")
		if !ok || guest != "22" {
			continue
		}
		n, err := strconv.Atoi(host)
		if err != nil {
			return 0, fmt.Errorf("boot-test: malformed host port %q in forward %q", host, p)
		}
		return n, nil
	}
	return 0, fmt.Errorf("boot-test: machine %q has no host forward to guest port 22 (an SSH forward like \"2222:22\" is required)", machine.Name)
}

// markerScanner tees QEMU's console output to an underlying writer while
// watching the byte stream for a marker. It closes found the first time the
// marker appears. QEMU writes from a single goroutine (the os/exec output
// copier), so writes are serialized; the retained tail spans writes so a
// marker split across two writes is still detected.
type markerScanner struct {
	w      io.Writer
	marker []byte
	tail   []byte
	found  chan struct{}
	once   sync.Once
}

func newMarkerScanner(w io.Writer, marker string) *markerScanner {
	return &markerScanner{w: w, marker: []byte(marker), found: make(chan struct{})}
}

func (m *markerScanner) Write(p []byte) (int, error) {
	// Tee to the caller's writer so the full boot log still reaches CI
	// output; ignore tee errors so a closed log never stalls the boot.
	_, _ = m.w.Write(p)

	m.tail = append(m.tail, p...)
	if bytes.Contains(m.tail, m.marker) {
		m.once.Do(func() { close(m.found) })
	}
	// Keep only enough trailing bytes to span a marker straddling writes.
	if cap := len(m.marker) + 256; len(m.tail) > cap {
		m.tail = m.tail[len(m.tail)-cap:]
	}
	return len(p), nil
}

// runBootTest boots the image headless under QEMU, waits for the serial
// console to reach the login prompt, SSHes in over the host:22 forward and
// runs a health command, then powers the guest off. It returns nil only
// when every stage succeeds within timeout.
func runBootTest(qemuBin string, args []string, sshPort int, timeout time.Duration, w io.Writer) error {
	if timeout <= 0 {
		timeout = defaultBootTestTimeout
	}
	deadline := time.Now().Add(timeout)

	fmt.Fprintf(w, "Boot test: %s (timeout %s, ssh 127.0.0.1:%d)\n", qemuBin, timeout, sshPort)

	// CommandContext kills QEMU when ctx is cancelled — on success, on any
	// failure path, and on timeout — so no guest is left running.
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, qemuBin, args...)
	scanner := newMarkerScanner(w, bootLoginMarker)
	cmd.Stdout = scanner
	cmd.Stderr = scanner
	// No stdin: the guest console is read-only here, and a nil stdin gives
	// QEMU's -nographic stdio an immediate EOF rather than blocking.

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("boot-test: starting QEMU: %w", err)
	}

	// Reap QEMU in the background. qemuDone is closed (not sent on) so both
	// the early-exit select below and the teardown wait can observe it; the
	// goroutine's write to qemuErr happens-before the close.
	var qemuErr error
	qemuDone := make(chan struct{})
	go func() { qemuErr = cmd.Wait(); close(qemuDone) }()

	// Teardown: stop QEMU and wait for it to actually exit before returning,
	// so its host port forwards are released and no guest is orphaned (a
	// bare cancel() returns before the SIGKILL is reaped). Reading the
	// already-closed qemuDone in the early-exit path returns immediately.
	defer func() {
		cancel()
		select {
		case <-qemuDone:
		case <-time.After(10 * time.Second):
		}
	}()

	select {
	case <-scanner.found:
		fmt.Fprintf(w, "\nBoot test: reached login prompt; connecting over SSH...\n")
	case <-qemuDone:
		return fmt.Errorf("boot-test: QEMU exited before reaching the login prompt: %w", qemuErr)
	case <-time.After(time.Until(deadline)):
		return fmt.Errorf("boot-test: timed out after %s waiting for the login prompt", timeout)
	}

	// SSH phase. sshd may still be coming up just after the login prompt,
	// so retry the dial until the deadline.
	out, err := sshHealthCheck(ctx, sshPort, deadline)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Boot test: SSH health check passed:\n%s\n", strings.TrimRight(out, "\n"))

	// Success — cancel() (deferred) powers the guest off.
	fmt.Fprintln(w, "Boot test: PASS")
	return nil
}

// sshHealthCheck connects to root@127.0.0.1:port with an empty password
// (dev images leave root passwordless and enable PermitEmptyPasswords),
// runs a health command, and returns its combined output. It retries the
// dial until deadline so sshd has time to finish starting.
func sshHealthCheck(ctx context.Context, port int, deadline time.Time) (string, error) {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // ephemeral localhost guest
		Timeout:         5 * time.Second,
	}

	var client *ssh.Client
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", fmt.Errorf("boot-test: cancelled before SSH connected")
		}
		c, err := ssh.Dial("tcp", addr, cfg)
		if err == nil {
			client = c
			break
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	if client == nil {
		return "", fmt.Errorf("boot-test: could not SSH to %s before timeout: %w", addr, lastErr)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("boot-test: opening SSH session: %w", err)
	}
	defer session.Close()

	const healthCmd = "uname -a"
	out, err := session.CombinedOutput(healthCmd)
	if err != nil {
		return string(out), fmt.Errorf("boot-test: health command %q failed: %w\n%s", healthCmd, err, out)
	}
	return string(out), nil
}
