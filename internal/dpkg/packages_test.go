package dpkg

import (
	"strings"
	"testing"
)

// realisticFixture is two real Debian bookworm Packages stanzas
// (trimmed). Two entries make the blank-line separator observable.
const realisticFixture = `Package: openssh-server
Source: openssh
Version: 1:9.2p1-2+deb12u6
Installed-Size: 1872
Maintainer: Debian OpenSSH Maintainers <debian-ssh@lists.debian.org>
Architecture: arm64
Pre-Depends: dpkg (>= 1.17.5), debconf (>= 0.5) | debconf-2.0
Depends: adduser (>= 3.9), dpkg (>= 1.9.0), libpam-modules (>= 0.72-9), libpam-runtime (>= 0.76-14), lsb-base (>= 4.1+Debian3), openssh-client (= 1:9.2p1-2+deb12u6), openssh-sftp-server, procps, ucf (>= 0.28), debconf (>= 0.5) | debconf-2.0, libaudit1 (>= 1:2.2.1), libc6 (>= 2.34), libcom-err2 (>= 1.43.9), libcrypt1 (>= 1:4.1.0), libgssapi-krb5-2 (>= 1.17), libkrb5-3 (>= 1.6.dfsg.2), libpam0g (>= 0.99.7.1), libselinux1 (>= 3.1~), libssl3 (>= 3.0.0), libsystemd0, libwrap0 (>= 7.6-4~), zlib1g (>= 1:1.1.4)
Recommends: default-logind | logind | libpam-systemd, ncurses-term, xauth
Suggests: molly-guard, monkeysphere, ssh-askpass, ufw
Conflicts: sftp, ssh-krb5
Breaks: openssh-client (<< 1:8.0p1-2)
Replaces: openssh-client (<< 1:7.9p1-8), ssh, ssh-krb5
Provides: ssh-server
Section: net
Priority: optional
Description: secure shell (SSH) server, for secure access from remote machines
Homepage: http://www.openssh.com/
Filename: pool/main/o/openssh/openssh-server_9.2p1-2+deb12u6_arm64.deb
Size: 458852
MD5sum: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
SHA256: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

Package: libc6
Source: glibc
Version: 2.36-9+deb12u9
Installed-Size: 13608
Maintainer: GNU Libc Maintainers <debian-glibc@lists.debian.org>
Architecture: arm64
Multi-Arch: same
Depends: libgcc-s1, libcrypt1 (>= 1:4.4.10-10~)
Recommends: libidn2-0 (>= 2.0.5~)
Suggests: glibc-doc, debconf | debconf-2.0, libc-l10n, locales
Breaks: hurd (<< 1:0.9.git20170825-3), iraf-fitsutil (<< 2018.07.06-4), libtirpc1 (<< 0.2.3), locales (<< 2.36), locales-all (<< 2.36), nocache (<< 1.1-1~), nscd (<< 2.36)
Provides: libc-l10n (= 2.36-9+deb12u9), libc6-2.36
Section: libs
Priority: optional
Multi-Arch: same
Description: GNU C Library: Shared libraries
Homepage: https://www.gnu.org/software/libc/libc.html
Filename: pool/main/g/glibc/libc6_2.36-9+deb12u9_arm64.deb
Size: 2710936
MD5sum: cccccccccccccccccccccccccccccccc
SHA256: dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd
`

func TestParseIndex_Realistic(t *testing.T) {
	entries, err := ParseIndex(strings.NewReader(realisticFixture))
	if err != nil {
		t.Fatalf("ParseIndex: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: got %d, want 2", len(entries))
	}

	srv := entries[0]
	if srv.Package != "openssh-server" {
		t.Errorf("Package: got %q", srv.Package)
	}
	if srv.Version != "1:9.2p1-2+deb12u6" {
		t.Errorf("Version: got %q", srv.Version)
	}
	if srv.Architecture != "arm64" {
		t.Errorf("Architecture: got %q", srv.Architecture)
	}
	if srv.InstalledSize != 1872 {
		t.Errorf("InstalledSize: got %d", srv.InstalledSize)
	}
	if srv.Size != 458852 {
		t.Errorf("Size: got %d", srv.Size)
	}
	if srv.Filename != "pool/main/o/openssh/openssh-server_9.2p1-2+deb12u6_arm64.deb" {
		t.Errorf("Filename: got %q", srv.Filename)
	}
	if srv.SHA256 == "" {
		t.Errorf("SHA256: got empty")
	}
	if srv.Provides != "ssh-server" {
		t.Errorf("Provides: got %q", srv.Provides)
	}
	if !strings.Contains(srv.Depends, "openssh-client") {
		t.Errorf("Depends: missing openssh-client; got %q", srv.Depends)
	}
	if !strings.Contains(srv.PreDepends, "dpkg") {
		t.Errorf("PreDepends: missing dpkg; got %q", srv.PreDepends)
	}

	libc := entries[1]
	if libc.Package != "libc6" {
		t.Errorf("libc Package: got %q", libc.Package)
	}
	if libc.MultiArch != "same" {
		t.Errorf("MultiArch: got %q", libc.MultiArch)
	}
	if !strings.Contains(libc.Provides, "libc-l10n") {
		t.Errorf("libc Provides: missing libc-l10n; got %q", libc.Provides)
	}
}

func TestParseIndex_MissingPackage(t *testing.T) {
	input := "Version: 1.0\nArchitecture: arm64\n"
	_, err := ParseIndex(strings.NewReader(input))
	if err == nil {
		t.Fatal("ParseIndex: expected error for missing Package field")
	}
}

func TestParseIndex_Empty(t *testing.T) {
	entries, err := ParseIndex(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseIndex on empty input: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries on empty input: got %d, want 0", len(entries))
	}
}
