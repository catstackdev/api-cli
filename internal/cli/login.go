package cli

import (
	"fmt"
	"sort"

	"cybercat/api/internal/httpc"
	"cybercat/api/internal/jsonbody"
	"cybercat/api/internal/render"
	"cybercat/api/internal/session"

	"github.com/spf13/cobra"
)

const (
	defaultTokenPath    = ".data.token"
	defaultGQLTokenPath = ".data.login.accessToken"
)

// loginFromConfig builds the login request of the resolved env from its
// [env.<name>.login] block. Extra -V pairs override the configured variables.
// The credentials in vars are value references, resolved only here.
func (a *app) loginFromConfig(o *reqOpts, extra []string, dry bool) (httpc.Spec, string, error) {
	rs, prof := a.resolve()
	envName := rs.Name
	if envName == "" {
		return httpc.Spec{}, "", fmt.Errorf("no env selected: pass --env NAME, run `api env use NAME`, or set default_env")
	}
	l := prof.Login
	if l == nil {
		return httpc.Spec{}, "", fmt.Errorf("no login configured for env %q: add [env.%s.login] to the GLOBAL config (api config init --global), or give a path / --gql", envName, envName)
	}
	if err := l.Complete(); err != nil {
		return httpc.Spec{}, "", fmt.Errorf("env %q: %w", envName, err)
	}
	if rs.BaseURL == "" {
		return httpc.Spec{}, "", fmt.Errorf("no server for env %q: run `api env load %s` first (it takes the URL from your .env), or add url = \"https://…\" under [env.%s]", envName, envName, envName)
	}

	// Where the request goes decides which secrets may be used: work it out
	// BEFORE any reference is resolved.
	var target, tokenPath string
	if l.GQL != "" {
		target = gqlTarget(rs.GraphQLURL, l.Endpoint)
		tokenPath = defaultGQLTokenPath
	} else {
		target = l.Path
		tokenPath = defaultTokenPath
	}
	if l.TokenPath != "" {
		tokenPath = l.TokenPath
	}
	// A login whose server is NOT fixed by your config (it came from a project
	// file, or has no url and uses the one your .env gave the shell) may only use
	// Keychain secrets bound to that server: the url could be a cloned repo's.
	source := srcGlobal
	if l.FromProject || prof.URL == "" {
		source = srcProject
	}
	resolve := jsonbody.Resolver(a.resolverFor(source, a.destOrigin(a.envName, target, o)))
	if dry {
		resolve = maskRef
	}

	keys := make([]string, 0, len(l.Vars))
	for k := range l.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys)+len(extra))
	for _, k := range keys {
		pairs = append(pairs, k+"="+l.Vars[k])
	}
	pairs = append(pairs, extra...)
	varsJSON, err := jsonbody.BuildWith(pairs, false, true, resolve)
	if err != nil {
		return httpc.Spec{}, "", err
	}
	payload := varsJSON
	if l.GQL != "" {
		if payload, err = gqlBody(l.GQL, varsJSON); err != nil {
			return httpc.Spec{}, "", err
		}
	}
	spec, err := a.newSpec("POST", target, o)
	if err != nil {
		return httpc.Spec{}, "", err
	}
	setJSONBody(&spec, payload)
	return spec, tokenPath, nil
}

func (a *app) loginCmd() *cobra.Command {
	o := &reqOpts{}
	var (
		emit     bool
		gql      bool
		endpoint string
		vars     []string
	)
	cmd := &cobra.Command{
		Use:   "login [<path> [body] | --gql <mutation>] [.token-path]",
		Short: "Log in (REST or GraphQL) and store the token as API_TOKEN",
		Long: `Send credentials, pull the token out of the response with a jq path and
export it as API_TOKEN.

With no arguments, the login of the selected env is used: the [env.<name>.login]
block of your global config, with credentials taken from value references
(@keychain:NAME, @env:NAME, @prompt). Set it up once, then just run "api login":

  api secret set upscale-dev        # asks once, stores in the Keychain
  api config set env.dev.login.gql 'mutation($input: LoginInput!) { login(input: $input) { accessToken } }' --global
  api config set env.dev.login.endpoint v2/auth --global
  api config set env.dev.login.vars.input.username me@x.com --global
  api config set env.dev.login.vars.input.password @keychain:upscale-dev --global
  api login --env dev

REST (default with a path): POST <path> with a JSON body; token path defaults to ` + defaultTokenPath + `.
GraphQL (--gql): send a login mutation like "api gql" does (same query / @file /
stdin / -V handling, -e endpoint); token path defaults to ` + defaultGQLTokenPath + `.
-V values accept references too: -V input.password=@keychain:NAME.

A child process cannot change its parent shell, so --emit prints the export
statement for the shell to eval; the zsh "api" function does that for you.
No bearer token is sent on the login call: a stale one can turn it into a 401.
--dry shows the request with references unresolved and sends nothing.`,
		Example: `  api login                                  # configured login of the current env
  api login --env thien
  api login /auth/login '{"email":"x","password":"y"}' '.data.access_token'
  api json email=x password=@keychain:dev | api login /auth/login '.data.access_token'
  api login --gql -e v2/auth @login.graphql -V input.username=me@x.com -V input.password=@prompt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.noAuth = true
			resolve := a.resolverFor(srcUser, a.destOrigin(a.envName, gqlTarget(a.session().GraphQLURL, endpoint), o))
			if o.dry { // a dry run must not read the Keychain, prompt or print a secret
				resolve = maskRef
			}

			var (
				spec      httpc.Spec
				tokenPath string
				err       error
			)
			switch {
			case gql:
				payload, filter, perr := a.gqlPayload(args, vars, resolve)
				if perr != nil {
					return perr
				}
				tokenPath = filter
				if tokenPath == "." {
					tokenPath = defaultGQLTokenPath
				}
				if spec, err = a.newSpec("POST", gqlTarget(a.session().GraphQLURL, endpoint), o); err != nil {
					return err
				}
				setJSONBody(&spec, payload)
			case len(args) == 0:
				if spec, tokenPath, err = a.loginFromConfig(o, vars, o.dry); err != nil {
					return err
				}
			default:
				target, bodyArg, tp, perr := splitPositionals(args, true)
				if perr != nil {
					return perr
				}
				tokenPath = tp
				if tokenPath == "." {
					tokenPath = defaultTokenPath
				}
				if spec, err = a.newSpec("POST", target, o); err != nil {
					return err
				}
				body, berr := a.resolveBody(o.data, bodyArg)
				if berr != nil {
					return berr
				}
				setJSONBody(&spec, body)
			}

			if o.dry { // never send credentials on a dry run
				fmt.Fprintln(a.io.Out, a.curlLine(spec, nil))
				return nil
			}

			a.say("⟳ logging in…")
			resp, err := a.send(cmd.Context(), spec, o)
			if err != nil {
				return err
			}
			if resp.Status >= 400 {
				_ = render.Write(a.io.Err, resp.Body, render.Options{Filter: "."})
				return &ExitError{Code: exitHTTP, Msg: fmt.Sprintf("✗ request failed: HTTP %d %s", resp.Status, resp.StatusText)}
			}

			token, err := render.First(resp.Body, tokenPath)
			if err != nil || token == "" {
				_ = render.Write(a.io.Err, resp.Body, render.Options{Filter: "."})
				msg := "✗ token not found at: " + tokenPath
				if gm := gqlError(resp.Body); gm != "" {
					msg = "✗ login failed: " + gm
				}
				return &ExitError{Code: exitGeneric, Msg: msg}
			}

			a.say(fmt.Sprintf("✓ API_TOKEN set: %s\n  path used: %s", session.MaskToken(token), tokenPath))
			if rs, _ := a.resolve(); rs.Name != "" {
				a.saveCachedToken(rs.Name, rs.BaseURL, spec.URL, token)
			}
			if emit {
				rs, prof := a.resolve()
				// The shell has no session of its own (the server came from
				// default_env): a token is never attached to a server chosen by
				// config alone, so adopt this env too or it would look logged out.
				noSession := a.getenv(session.EnvBaseURL) == "" && rs.Name != "" && rs.BaseURL != ""
				if a.envName != "" || noSession {
					// Logged in to another env than the shell's: switch the whole
					// session, or the next plain call would send this token to the
					// previous env's server.
					fmt.Fprintf(a.io.Out, "%s=%s; %s=%s; %s=%s; %s=%s\n",
						session.EnvBaseURL, session.Quote(rs.BaseURL),
						session.EnvGraphQLURL, session.Quote(rs.GraphQLURL),
						session.EnvName, session.Quote(rs.Name),
						session.EnvFrom, "toml")
					a.say("  (switched the shell to env " + rs.Name + ")")
				}
				fmt.Fprintln(a.io.Out, session.Export(session.EnvToken, token))
				if prof.TokenEnv != "" { // so `api diff` can authenticate this env too
					fmt.Fprintf(a.io.Out, "%s=%s\n", prof.TokenEnv, session.Quote(token))
				}
			} else {
				a.say("  (not exported: use the zsh `api login` function, or eval \"$(api login --emit ...)\")")
			}
			return nil
		},
		ValidArgsFunction: a.pathCompletion,
	}
	a.bindFlags(o, cmd, true)
	f := cmd.Flags()
	f.BoolVar(&emit, "emit", false, "print `export API_TOKEN=...` for the shell to eval")
	markShellEmit(cmd)
	f.BoolVar(&gql, "gql", false, "GraphQL login: send a mutation instead of a REST body")
	f.StringVarP(&endpoint, "endpoint", "e", "", "(--gql) path appended to the GraphQL base, or a full URL")
	f.StringArrayVarP(&vars, "var", "V", nil, "variable key=value or key:=json (repeatable); values may be @keychain:NAME / @env:NAME / @prompt")
	_ = cmd.RegisterFlagCompletionFunc("endpoint", a.endpointCompletion)
	return cmd
}
