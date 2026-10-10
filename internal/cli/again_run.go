package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cybercat/api/internal/history"
	"cybercat/api/internal/httpfile"

	"github.com/spf13/cobra"
)

// A request picked with `api openapi pick` or `api gql --pick` is kept as a file
// under .api/requests/, and the history remembers a POINTER to it (RunMethod,
// the file's name, the project and the server), never the query or a body.
// `api again` runs that file again, so the one place that knows how to run a
// request file stays `api run`.

// recordRun remembers a saved request that just ran successfully. url is the
// request's URL as written in the file: its origin is the server it went to, or
// the current one when it is relative.
func (a *app) recordRun(savedPath, url string) {
	name := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(savedPath), ".http"), ".rest")
	oc := origin(url)
	if oc == "" {
		oc = a.currentOrigin()
	}
	if oc == "" || !fileSafe.MatchString(name) {
		return
	}
	history.Record(history.Entry{Method: history.RunMethod, Path: name, Origin: oc, Root: findRoot()})
}

// hintRetry says how to try a failed pick again.
func (a *app) hintRetry() {
	a.say("  kept: `api again` retries it, `api again --edit` changes it first")
}

// savedRequestPath is <root>/.api/requests/<name>.http (or .rest) for a name
// taken from the history. It is built from the name alone: an arbitrary path,
// or a file of the same name elsewhere, must never be what runs.
func savedRequestPath(root, name string) (string, error) {
	if !fileSafe.MatchString(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("%q is not a saved request name", name)
	}
	dir := filepath.Join(root, filepath.FromSlash(requestsDir))
	for _, ext := range []string{".http", ".rest"} {
		p := filepath.Join(dir, name+ext)
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s/%s.http is gone", requestsDir, name)
}

// requestIsReadOnly decides whether a request may run without asking. It is
// judged on the text that will be sent. A body with a {{reference}} could hide
// a mutation, so it is never treated as read-only.
func requestIsReadOnly(r *httpfile.Request) bool {
	if readOnlyMethod(r.Method) {
		return true
	}
	if strings.ToUpper(r.Method) != "POST" || strings.Contains(r.Body, "{{") {
		return false
	}
	var b struct {
		Query *string `json:"query"`
	}
	return json.Unmarshal([]byte(r.Body), &b) == nil && b.Query != nil && isReadOnlyQuery(*b.Query)
}

// againOpts are the options of `api again` that only a saved request uses.
type againOpts struct {
	filter string
	edit   bool // open the saved request in the editor first
	yes    bool // do not ask before sending a request that changes data
	fresh  bool // start the editor from the skeleton the pick generated (implies edit)
}

// againRun repeats a RUN entry. Where it can run is checked first (the project
// and the server it was saved for), then what is in the file NOW decides
// whether to ask, as for the pick that created it.
func (a *app) againRun(cmd *cobra.Command, e history.Entry, ao againOpts, o *reqOpts) error {
	edit := ao.edit
	root := findRoot()
	switch cur := a.currentOrigin(); {
	case e.Root != root:
		return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %s belongs to the project %s: cd there to repeat it", e.Path, orUnset(e.Root))}
	case e.Origin == "" || e.Origin != cur:
		return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ %s went to %s, but the shell points at %s: switch (api env use ...) or run it by hand", e.Path, orUnset(e.Origin), orUnset(cur))}
	}
	path, err := savedRequestPath(root, e.Path)
	if err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ " + err.Error()}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	if ao.fresh {
		// start the editor from the skeleton the pick generated, not from your last version
		orig, err := os.ReadFile(path + ".orig")
		if err != nil {
			return &ExitError{Code: exitGeneric, Msg: "✗ no original skeleton was kept for " + e.Path + " (only picks saved by this version have one): pick it again"}
		}
		text, edit = string(orig), true
	}
	if edit {
		// a copy: what you change here runs once and is never written back
		edited, changed, err := a.editTempExt(e.Path, ".http", text)
		if err != nil {
			return err
		}
		if !changed || strings.TrimSpace(edited) == "" {
			a.say("  (cancelled: nothing run)")
			return &ExitError{Code: exitGeneric}
		}
		text = edited
	}
	file, err := httpfile.Parse([]byte(text))
	if err != nil {
		return &ExitError{Code: exitUsage, Msg: "✗ the request is not valid: " + err.Error()}
	}
	if len(file.Requests) != 1 {
		return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("✗ %s must hold exactly one request, found %d (use `api run %s`)", filepath.Base(path), len(file.Requests), e.Path)}
	}
	req := &file.Requests[0]
	if !requestIsReadOnly(req) && !ao.yes && !o.dry {
		if !a.confirm(fmt.Sprintf("✱ %s %s changes data on %s: run it?", strings.ToUpper(req.Method), req.URL, orUnset(a.session().BaseURL))) {
			a.say("  (not run)")
			return &ExitError{Code: exitGeneric}
		}
	}
	if a.run.asserts, err = compileAsserts(path, []*httpfile.Request{req}); err != nil {
		return err
	}
	a.say("→ " + strings.ToUpper(req.Method) + " " + req.URL + "  (" + requestsDir + "/" + filepath.Base(path) + ")")
	runErr := a.runRequest(cmd, runJob{path: path, file: file, req: req, filter: ao.filter}, o)
	if o.dry {
		return runErr
	}
	// What you saved in the editor stays, whether or not the run worked: fixing a
	// request is a few tries, and the next `again --edit` must start from the last
	// one. The version it replaces is kept once as <file>.bak.
	if edit && text != string(data) {
		if err := keepEdit(path, data, []byte(text)); err != nil {
			a.say("⚠ the edited request was not kept: " + err.Error())
		} else {
			a.say(fmt.Sprintf("✓ kept your edit in %s/%s (the previous version: %s.bak)", requestsDir, filepath.Base(path), filepath.Base(path)))
		}
	}
	a.recordRun(path, req.URL)
	return runErr
}

// keepEdit writes the edited request over its file, after saving the old text
// as <path>.bak (one level; it is not a *.http, so `api run --list` ignores it).
func keepEdit(path string, old, edited []byte) error {
	if err := writeFileAtomic(path+".bak", old, 0o600); err != nil {
		return err
	}
	return writeFileAtomic(path, edited, 0o600)
}
