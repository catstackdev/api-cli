package cli

import (
	"fmt"
	"path/filepath"

	"cybercat/api/internal/assert"
	"cybercat/api/internal/httpc"
	"cybercat/api/internal/httpfile"
)

// exitAssert is the exit code of a failed `# @assert`: a CI job can tell "the
// response was not what the file says" from an HTTP error (22) or a transport
// error (28/7/6).
const exitAssert = 3

// compiledAssert is one @assert, compiled and tied to its line.
type compiledAssert struct {
	*assert.Assertion
	line int
}

// compileAsserts compiles the @assert lines of every request about to run, so a
// typo exits with a usage error and the line number BEFORE the first request
// is sent: half a run against a real server is not an answer.
func compileAsserts(path string, reqs []*httpfile.Request) (map[*httpfile.Request][]compiledAssert, error) {
	out := map[*httpfile.Request][]compiledAssert{}
	for _, r := range reqs {
		for _, as := range r.Asserts {
			c, err := assert.Compile(as.Expr)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: @assert %q (request %q): %v", filepath.Base(path), as.Line, as.Expr, r.Name, err)
			}
			out[r] = append(out[r], compiledAssert{c, as.Line})
		}
	}
	return out, nil
}

// checkAsserts evaluates r's assertions on resp and prints one line each. The
// line carries the expression and a reason, never the response: see package
// assert. It returns the exit error for a failure, nil when all passed.
func (a *app) checkAsserts(r *httpfile.Request, resp *httpc.Response, list []compiledAssert) error {
	env := assert.Envelope{Status: resp.Status, Header: resp.Header, Body: resp.Body, Duration: resp.Duration}
	failed := 0
	for _, as := range list {
		res := as.Eval(env)
		a.run.assertTotal++
		if res.Pass {
			a.say("✓ " + as.Expr)
			continue
		}
		failed++
		a.run.assertFailed++
		a.say(fmt.Sprintf("✗ %s — %s (line %d)", as.Expr, res.Reason, as.line))
	}
	if failed == 0 {
		return nil
	}
	return &ExitError{Code: exitAssert, Msg: fmt.Sprintf("✗ %s: %d of %d assertions failed", r.Name, failed, len(list))}
}

// finishAsserted prints the response as usual, then lets the assertions decide
// the result: an asserted 404 is a pass, so the HTTP-status exit (22) is
// dropped. Any other failure to print (a bad filter) still counts.
func (a *app) finishAsserted(r *httpfile.Request, resp *httpc.Response, o *reqOpts, filter string, list []compiledAssert) error {
	printErr := a.finish(resp, o, filter, nil)
	if ee, ok := printErr.(*ExitError); ok && ee.Code == exitHTTP {
		printErr = nil
	}
	if err := a.checkAsserts(r, resp, list); err != nil {
		return err
	}
	return printErr
}
