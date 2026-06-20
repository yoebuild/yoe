package internal

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/yoebuild/yoe/internal/module"
	"github.com/yoebuild/yoe/internal/source"
	yoestar "github.com/yoebuild/yoe/internal/starlark"
)

func ListModules(dir string, w io.Writer) error {
	proj, err := yoestar.LoadProject(dir)
	if err != nil {
		return err
	}

	if len(proj.Modules) == 0 {
		fmt.Fprintln(w, "No modules declared in PROJECT.star")
		return nil
	}

	fmt.Fprintf(w, "%-40s %-12s %s\n", "Module", "Ref", "Status")
	for _, m := range proj.Modules {
		status := "not synced"
		if m.Local != "" {
			status = fmt.Sprintf("(local: %s)", m.Local)
		}
		ref := m.Ref
		if ref == "" {
			ref = "(none)"
		}
		fmt.Fprintf(w, "%-40s %-12s %s\n", m.URL, ref, status)
	}

	return nil
}

func ListModuleInfo(dir string, w io.Writer, opts ...yoestar.LoadOption) error {
	modules, err := yoestar.ResolveProjectModules(dir, opts...)
	if err != nil {
		return err
	}

	if len(modules) == 0 {
		fmt.Fprintln(w, "No modules declared in PROJECT.star")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tURL/PATH\tVERSION/REF\tSTATUS\tLAST SYNC")
	for _, m := range modules {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			m.Name,
			moduleInfoSource(m),
			moduleInfoRef(m),
			moduleInfoStatus(m),
			moduleInfoLastSync(m),
		)
	}
	return tw.Flush()
}

func moduleInfoSource(m yoestar.ResolvedModule) string {
	if m.Local != "" {
		if m.Path != "" {
			return fmt.Sprintf("%s (path: %s)", m.Local, m.Path)
		}
		return m.Local
	}
	if m.Path != "" {
		return fmt.Sprintf("%s (path: %s)", m.URL, m.Path)
	}
	return m.URL
}

func moduleInfoRef(m yoestar.ResolvedModule) string {
	if m.Ref != "" {
		return m.Ref
	}
	return "main"
}

func moduleInfoStatus(m yoestar.ResolvedModule) string {
	if m.Local != "" {
		return "local"
	}
	if !m.Available {
		return "not synced"
	}
	repo := moduleInfoRepoDir(m)
	state := module.ReadState(repo)
	if source.IsDev(state) {
		live, err := source.DetectState(repo, state)
		if err == nil && live != source.StateEmpty {
			return string(live)
		}
		return string(state)
	}
	return "synced"
}

func moduleInfoLastSync(m yoestar.ResolvedModule) string {
	if m.Local != "" {
		return "-"
	}
	if !m.Available {
		return "never"
	}
	t, ok := module.LastSyncTime(moduleInfoRepoDir(m))
	if !ok {
		return "unknown"
	}
	return t.Local().Format("2006-01-02 15:04:05 -0700")
}

func moduleInfoRepoDir(m yoestar.ResolvedModule) string {
	if m.CloneDir != "" {
		return m.CloneDir
	}
	return m.Dir
}
