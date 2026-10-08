package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// cacheSet is one kind of cache: a directory and the exact file patterns api
// writes there. Only those patterns are ever deleted, never the directory: both
// directories can be moved by an environment variable, so "remove everything
// in it" could be pointed at something that is not ours.
type cacheSet struct {
	name     string
	dir      string
	patterns []string
}

func (a *app) cacheSets() []cacheSet {
	return []cacheSet{
		{"openapi", a.openapiCacheDir(), []string{"*.json"}},
		{"schema", a.ugqlCacheDir(), []string{"*.json", "index.txt", "preview.jq", ".env"}},
	}
}

// files lists the regular files api owns in the set; symlinks are skipped
// (removing one is harmless, but it says nothing about what it points at).
func (c cacheSet) files() []os.FileInfo {
	var out []os.FileInfo
	seen := map[string]bool{}
	for _, pat := range c.patterns {
		m, _ := filepath.Glob(filepath.Join(c.dir, pat))
		for _, p := range m {
			st, err := os.Lstat(p)
			if err != nil || !st.Mode().IsRegular() || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, namedInfo{st, p})
		}
	}
	return out
}

type namedInfo struct {
	os.FileInfo
	path string
}

func (a *app) cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Show or clear the caches (OpenAPI indexes, GraphQL schemas)",
		Long: `The caches are safe to delete: api rebuilds them with ` + "`api openapi sync`" + ` and
` + "`api schema sync`" + `. History (` + "`api history clear`" + `), cached login tokens
(` + "`api logout --all`" + `) and ` + requestsDir + `/ are not caches and are never touched here.`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Size and age of each cache",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			var rows [][]string
			for _, c := range a.cacheSets() {
				files := c.files()
				var size int64
				var newest time.Time
				for _, f := range files {
					size += f.Size()
					if f.ModTime().After(newest) {
						newest = f.ModTime()
					}
				}
				age := "-"
				if len(files) > 0 {
					age = ageOf(newest)
				}
				rows = append(rows, []string{c.name, strconv.Itoa(len(files)), strconv.FormatInt(size, 10) + " B", age, c.dir})
			}
			ui.Table(a.io.Out, []string{"CACHE", "FILES", "SIZE", "NEWEST", "DIR"}, rows, a.colorOut())
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:       "clear [openapi|schema|all]",
		Short:     "Delete a cache (default: all)",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"openapi", "schema", "all"},
		RunE: func(cmd *cobra.Command, args []string) error {
			which := "all"
			if len(args) == 1 {
				which = args[0]
			}
			if which != "all" && which != "openapi" && which != "schema" {
				return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("✗ unknown cache %q (openapi, schema or all)", which)}
			}
			removed := 0
			for _, c := range a.cacheSets() {
				if which != "all" && which != c.name {
					continue
				}
				for _, f := range c.files() {
					if err := os.Remove(f.(namedInfo).path); err != nil && !os.IsNotExist(err) {
						return err
					}
					removed++
				}
			}
			a.say(fmt.Sprintf("✓ %d cache file(s) removed", removed))
			return nil
		},
	})
	return cmd
}

func ageOf(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
