package cli

import (
	"cybercat/api/internal/httpfile"
	"cybercat/api/internal/openapi"
	"cybercat/api/internal/session"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// pathParam finds the {id} placeholders of an OpenAPI path.
var pathParam = regexp.MustCompile(`\{([^{}]+)\}`)

// runnerVar is the .http runner's own {{variable}} syntax.
var runnerVar = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// pickMarker starts the instruction lines this command adds to the file you edit;
// they are dropped again before the request is run or saved.
const pickMarker = "# openapi-pick:"

// pickCommand is the `api` command line for an operation, and the path
// parameters the user still has to fill in. Body verbs get a '{}' to start from.
func pickCommand(op openapi.Operation) (line string, params []string) {
	for _, m := range pathParam.FindAllStringSubmatch(op.Path, -1) {
		params = append(params, "{"+m[1]+"}")
	}
	p := shellWord(op.Path)
	switch op.Method {
	case "GET":
		line = "api get " + p
	case "DELETE":
		line = "api del " + p
	case "POST", "PUT", "PATCH":
		line = "api " + strings.ToLower(op.Method) + " " + p + " '{}'"
	case "HEAD":
		line = "api headers " + p
	default:
		line = "api get " + p + " -- -X " + op.Method
	}
	return line, params
}

var slugUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// pickSlug names the saved request: the operationId, else method + path words.
func pickSlug(op openapi.Operation) string {
	if op.OperationID != "" {
		return op.OperationID
	}
	s := strings.ToLower(op.Method) + "-" + strings.Trim(slugUnsafe.ReplaceAllString(op.Path, "-"), "-")
	return strings.Trim(s, "-")
}

// pickFile is the .http file the editor opens: one request, with a body
// skeleton from the spec for POST/PUT/PATCH. The summary is server data: it goes
// in as "# openapi: <text>" so it can never start an annotation (# @assert, # @filter)
// or a heading, and it was flattened to one line when the spec was indexed.
func pickFile(op openapi.Operation, slug string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n", slug)
	b.WriteString(pickMarker + " save to run, quit without saving to cancel. Fill in any {placeholder}.\n")
	b.WriteString(pickMarker + " Anything that changes data asks before it is sent.\n")
	if op.Summary != "" {
		fmt.Fprintf(&b, "# openapi: %s\n", op.Summary)
	}
	fmt.Fprintf(&b, "%s %s\nAccept: application/json\n", op.Method, op.Path)
	switch op.Method {
	case "POST", "PUT", "PATCH":
		body := op.Body
		if body == "" {
			body = "{}"
		}
		pretty, ok := prettyBody(body)
		if !ok {
			pretty = "{}"
		}
		b.WriteString("Content-Type: application/json\n\n" + pretty + "\n")
	}
	return b.String()
}

// stripPickMarkers drops the instruction lines pickFile added.
func stripPickMarkers(doc string) string {
	var out []string
	for _, l := range strings.Split(doc, "\n") {
		if !strings.HasPrefix(l, pickMarker) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func readOnlyMethod(m string) bool {
	switch strings.ToUpper(m) {
	case "GET", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// pickFlags are the options of the picker.
type pickFlags struct{ yes, noSave, printOnly bool }

func bindPickFlags(cmd *cobra.Command, pf *pickFlags) {
	f := cmd.Flags()
	f.BoolVar(&pf.yes, "yes", false, "do not ask before sending a request that changes data")
	f.BoolVar(&pf.noSave, "no-save", false, "do not keep the request under "+requestsDir+"/")
	f.BoolVar(&pf.printOnly, "print", false, "print the api command for the picked operation: no editor, nothing sent")
}

const pickLong = `Pick an operation with fzf (the preview shows its summary and body skeleton), edit
the generated request in your editor, save to run it. It is the OpenAPI twin of
"api gql": quit the editor without saving to cancel, and anything that changes data
asks first ("✱ POST /users changes data on <server>: run it? [y/N]"). What you SAVED
decides whether to ask, not what you picked.

The request is a .http file: the body skeleton comes from the spec's request schema,
{placeholders} in the path must be filled in, and you may add # @assert lines. After
you confirm it is kept as ` + requestsDir + `/<name>.http (never overwriting an earlier one),
so "api run <name>" repeats it. --no-save keeps nothing; --print only prints the
api command for the pick. Needs a synced index (api openapi sync).`

func (a *app) openapiPickCmd() *cobra.Command {
	o := &reqOpts{}
	pf := &pickFlags{}
	cmd := &cobra.Command{
		Use:   "pick [text]",
		Short: "Pick an operation with fzf, edit the request, save and run it",
		Long:  pickLong,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) == 1 {
				text = args[0]
			}
			return a.openapiPick(cmd, text, *pf, o)
		},
	}
	a.bindFlags(o, cmd, false)
	bindPickFlags(cmd, pf)
	return cmd
}

func (a *app) openapiShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <METHOD> <path>",
		Short: "Show one cached operation (summary and body skeleton): the picker's preview",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := a.requireOpenAPI()
			if err != nil {
				return err
			}
			for _, op := range idx.Operations {
				if !strings.EqualFold(op.Method, args[0]) || op.Path != args[1] {
					continue
				}
				fmt.Fprintf(a.io.Out, "%s %s\n", op.Method, op.Path)
				if op.Summary != "" {
					fmt.Fprintln(a.io.Out, op.Summary)
				}
				if op.OperationID != "" {
					fmt.Fprintln(a.io.Out, "operationId: "+op.OperationID)
				}
				if pretty, ok := prettyBody(op.Body); op.Body != "" && ok {
					fmt.Fprintf(a.io.Out, "\nrequest body (skeleton):\n%s\n", pretty)
				}
				return nil
			}
			return &ExitError{Code: exitGeneric, Msg: "✗ no such operation in the index: " + args[0] + " " + args[1]}
		},
	}
}

// openapiPick lets the user choose one cached operation, then (unless --print)
// edits, saves and runs it.
func (a *app) openapiPick(cmd *cobra.Command, text string, pf pickFlags, o *reqOpts) error {
	idx, err := a.requireOpenAPI()
	if err != nil {
		return err
	}
	byLine := map[string]openapi.Operation{}
	var lines []string
	needle := strings.ToLower(text)
	for _, op := range idx.Operations {
		if !matchesFilter(op, needle) {
			continue
		}
		line := strings.TrimSpace(fmt.Sprintf("%-7s %s  %s", op.Method, op.Path, op.Summary))
		byLine[line] = op
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return &ExitError{Code: exitGeneric, Msg: "✗ nothing matches"}
	}
	if _, err := exec.LookPath("fzf"); err != nil {
		return &ExitError{Code: exitGeneric, Msg: "✗ picking needs fzf in PATH (brew install fzf); `api openapi list` works without it"}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	line, _, cancelled, err := a.pickWithFzf(lines, "--prompt=openapi> ",
		"--preview="+session.Quote(exe)+" openapi show {1} {2}", "--preview-window=right,60%,wrap")
	if err != nil {
		return err
	}
	if cancelled {
		return &ExitError{Code: exitGeneric}
	}
	op, ok := byLine[line]
	if !ok {
		return fmt.Errorf("could not read fzf's choice")
	}
	if pf.printOnly {
		cmdline, params := pickCommand(op)
		fmt.Fprintln(a.io.Out, cmdline)
		if len(params) > 0 {
			a.say("→ replace " + strings.Join(params, ", ") + " in the path before running it")
		}
		return nil
	}
	return a.openapiEditAndRun(cmd, op, pf, o)
}

func (a *app) openapiEditAndRun(cmd *cobra.Command, op openapi.Operation, pf pickFlags, o *reqOpts) error {
	slug := pickSlug(op)
	edited, changed, err := a.editTempExt(slug, ".http", pickFile(op, slug))
	if err != nil {
		return err
	}
	if !changed || strings.TrimSpace(edited) == "" {
		a.say("  (cancelled: nothing saved)")
		return &ExitError{Code: exitGeneric}
	}
	doc := stripPickMarkers(edited)
	file, err := httpfile.Parse([]byte(doc))
	if err != nil {
		return &ExitError{Code: exitUsage, Msg: "✗ the request is not valid: " + err.Error()}
	}
	if len(file.Requests) != 1 {
		return &ExitError{Code: exitUsage, Msg: fmt.Sprintf("✗ the file must hold exactly one request, found %d", len(file.Requests))}
	}
	req := &file.Requests[0]
	if m := pathParam.FindString(runnerVar.ReplaceAllString(req.URL, "")); m != "" {
		return &ExitError{Code: exitUsage, Msg: "✗ replace " + m + " in the path before running it (nothing was sent or saved)"}
	}

	// what was SAVED decides whether to ask, not what was picked: it may have been edited
	if !readOnlyMethod(req.Method) && !pf.yes && !o.dry {
		dest := orUnset(a.session().BaseURL)
		if strings.HasPrefix(req.URL, "http://") || strings.HasPrefix(req.URL, "https://") {
			dest = origin(req.URL)
		}
		if !a.confirm(fmt.Sprintf("✱ %s %s changes data on %s: run it?", strings.ToUpper(req.Method), req.URL, dest)) {
			a.say("  (not run, not saved)")
			return &ExitError{Code: exitGeneric}
		}
	}

	path := filepath.Join(findRoot(), "openapi-pick.http") // only where a "< file" body is resolved from
	kept := false
	if !pf.noSave && !o.dry {
		saved, err := savePicked(slug, doc)
		if err != nil {
			return err
		}
		path, kept = saved, true
		// the generated skeleton, for `api again --default` (best effort)
		_ = os.WriteFile(saved+".orig", []byte(strings.TrimRight(stripPickMarkers(pickFile(op, slug)), "\n")+"\n"), 0o600)
		a.say(fmt.Sprintf("✓ saved %s/%s (api run %s)", requestsDir, filepath.Base(saved), strings.TrimSuffix(filepath.Base(saved), ".http")))
	}
	if a.asserts, err = compileAsserts(path, []*httpfile.Request{req}); err != nil {
		return err
	}
	a.say("→ " + strings.ToUpper(req.Method) + " " + req.URL)
	err = a.runRequest(cmd, path, file, req, map[string]string{}, o, "", o.trustHosts, false)
	if kept {
		// remembered even when it failed (a 400 on a body you still have to fix):
		// `api again` retries it, `api again --edit` fixes it first
		a.recordRun(path, req.URL)
		if err != nil {
			a.hintRetry()
		}
	}
	return err
}

// savePicked writes the request under .api/requests/, never over an earlier file
// (get-users.http, get-users-2.http, ...).
func savePicked(slug, doc string) (string, error) {
	dir := filepath.Join(findRoot(), filepath.FromSlash(requestsDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for n := 1; n < 1000; n++ {
		name := slug + ".http"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.http", slug, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.WriteString(strings.TrimRight(doc, "\n") + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return path, werr
	}
	return "", fmt.Errorf("too many saved requests named %s", slug)
}
