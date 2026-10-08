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

// cacheSet is one kind of cache. Only files api itself writes are ever deleted,
// never a directory: both locations can be moved by an environment variable, so
// "remove everything in it" could be pointed at something that is not ours.
type cacheSet struct {
	name     string
	dirs     []string // directories that may hold the files
	patterns []string
	marker   string // a file api always writes there; without it the directory is not ours
	links    []string
}

// cachedFile is one file (or the `current` link) a clear would remove.
type cachedFile struct {
	path string
	size int64
	mod  time.Time
}

func (a *app) cacheSets() []cacheSet {
	schemaDirs := []string{a.ugqlRoot()}
	var links []string
	if a.ugqlNested() {
		// one directory per server, and the link the fzf / tv browsers read
		sub, _ := filepath.Glob(filepath.Join(a.ugqlRoot(), "*"))
		for _, d := range sub {
			if st, err := os.Lstat(d); err == nil && st.IsDir() {
				schemaDirs = append(schemaDirs, d)
			}
		}
		links = []string{filepath.Join(a.ugqlRoot(), "current"), filepath.Join(a.ugqlRoot(), ".current.tmp")}
	}
	return []cacheSet{
		{name: "openapi", dirs: []string{a.openapiCacheDir()}, patterns: []string{"http_*.json", "https_*.json"}},
		{name: "schema", dirs: schemaDirs, patterns: []string{"*.json", "index.txt", "preview.jq", ".env"}, marker: "preview.jq", links: links},
	}
}

// files lists what the set owns: regular files only (a symlink says nothing
// about what it points at), in directories that hold the marker file.
func (c cacheSet) files() []cachedFile {
	var out []cachedFile
	seen := map[string]bool{}
	for _, dir := range c.dirs {
		if c.marker != "" {
			if st, err := os.Lstat(filepath.Join(dir, c.marker)); err != nil || !st.Mode().IsRegular() {
				continue
			}
		}
		for _, pat := range c.patterns {
			m, _ := filepath.Glob(filepath.Join(dir, pat))
			for _, p := range m {
				st, err := os.Lstat(p)
				if err != nil || !st.Mode().IsRegular() || seen[p] {
					continue
				}
				seen[p] = true
				out = append(out, cachedFile{p, st.Size(), st.ModTime()})
			}
		}
	}
	// the `current` pointer is a symlink api made: it goes only when it is one
	// and points at a sibling directory by a plain name
	for _, l := range c.links {
		if st, err := os.Lstat(l); err == nil && st.Mode()&os.ModeSymlink != 0 {
			if t, err := os.Readlink(l); err == nil && fileSafe.MatchString(t) {
				out = append(out, cachedFile{l, 0, st.ModTime()})
			}
		}
	}
	return out
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
					size += f.size
					if f.mod.After(newest) {
						newest = f.mod
					}
				}
				age := "-"
				if len(files) > 0 {
					age = ageOf(newest)
				}
				rows = append(rows, []string{c.name, strconv.Itoa(len(files)), strconv.FormatInt(size, 10) + " B", age, c.dirs[0]})
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
					if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
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
