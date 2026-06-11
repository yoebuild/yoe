package dpkg

import "testing"

func TestProvidesTable_BareName(t *testing.T) {
	entries := []Entry{
		{Package: "openssh-server", Version: "1:9.2p1-1"},
		{Package: "libc6", Version: "2.36-9"},
	}
	tbl := BuildProvidesTable(entries)
	if e := tbl.Lookup("libc6"); e == nil {
		t.Errorf("Lookup(libc6): nil")
	} else if e.Package != "libc6" {
		t.Errorf("Lookup(libc6): got %q", e.Package)
	}
	if tbl.Lookup("absent") != nil {
		t.Errorf("Lookup(absent): want nil")
	}
}

func TestProvidesTable_Virtual(t *testing.T) {
	entries := []Entry{
		{Package: "postfix", Version: "3.7.0", Provides: "mail-transport-agent"},
		{Package: "exim4", Version: "4.96", Provides: "mail-transport-agent"},
	}
	tbl := BuildProvidesTable(entries)
	if e := tbl.Lookup("mail-transport-agent"); e == nil {
		t.Errorf("Lookup virtual: nil")
	}
}

func TestProvidesTable_NewerWins(t *testing.T) {
	entries := []Entry{
		{Package: "lib-old", Version: "1.0.0", Provides: "lib-iface"},
		{Package: "lib-new", Version: "2.0.0", Provides: "lib-iface"},
	}
	tbl := BuildProvidesTable(entries)
	winner := tbl.Lookup("lib-iface")
	if winner == nil {
		t.Fatalf("Lookup virtual: nil")
	}
	if winner.Package != "lib-new" {
		t.Errorf("newer wins: got %q, want lib-new", winner.Package)
	}
}

func TestProvidesTable_VersionedProvides(t *testing.T) {
	entries := []Entry{
		{Package: "libc6", Version: "2.36-9", Provides: "libc-l10n (= 2.36-9), libc6-2.36"},
	}
	tbl := BuildProvidesTable(entries)
	if tbl.Lookup("libc-l10n") == nil {
		t.Errorf("Lookup(libc-l10n): nil — versioned provides not stripped")
	}
	if tbl.Lookup("libc6-2.36") == nil {
		t.Errorf("Lookup(libc6-2.36): nil")
	}
}
