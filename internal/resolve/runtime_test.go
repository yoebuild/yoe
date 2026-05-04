package resolve_test

import (
	"sort"
	"testing"

	"github.com/yoebuild/yoe/internal/resolve"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func TestRuntimeClosure_Sqlite(t *testing.T) {
	proj := &yoestar.Project{
		Units: map[string]*yoestar.Unit{
			"sqlite":   {Name: "sqlite",   RuntimeDeps: []string{"musl", "readline"}},
			"musl":     {Name: "musl"},
			"readline": {Name: "readline", RuntimeDeps: []string{"ncurses"}},
			"ncurses":  {Name: "ncurses",  RuntimeDeps: []string{"musl"}},
			"unrelated":{Name: "unrelated"},
		},
		Provides: map[string]string{},
	}
	got := resolve.RuntimeClosure(proj, []string{"sqlite"})
	sort.Strings(got)
	want := []string{"musl", "ncurses", "readline", "sqlite"}
	if len(got) != len(want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("closure[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRuntimeClosure_RoutesProvides(t *testing.T) {
	proj := &yoestar.Project{
		Units: map[string]*yoestar.Unit{
			"app":         {Name: "app",         RuntimeDeps: []string{"linux"}},
			"linux-rpi4":  {Name: "linux-rpi4",  RuntimeDeps: []string{"musl"}},
			"musl":        {Name: "musl"},
		},
		Provides: map[string]string{"linux": "linux-rpi4"},
	}
	got := resolve.RuntimeClosure(proj, []string{"app"})
	sort.Strings(got)
	want := []string{"app", "linux-rpi4", "musl"}
	if len(got) != len(want) {
		t.Fatalf("closure = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("closure[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
