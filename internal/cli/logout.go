package cli

import (
	"fmt"

	"cybercat/api/internal/session"

	"github.com/spf13/cobra"
)

func (a *app) logoutCmd() *cobra.Command {
	var emit, all bool
	cmd := &cobra.Command{
		Use:   "logout [--env NAME] [--all]",
		Short: "Forget this shell's token and the cached token of the env",
		Long: `Unsets API_TOKEN in this shell and deletes the token cached for the env
(defaults.token_cache). With --env NAME, only that env's cached token and its
token_env variable are forgotten; this shell's API_TOKEN stays unless NAME is
the env the shell is on. --all forgets the cached token of every env in your
config. Tokens cached for envs that are no longer in your config can be removed
with: api secret rm token.<env>

Like login, it changes your shell: run it through the zsh "api" function.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !emit {
				return &ExitError{Code: exitUsage, Msg: "✗ use the zsh `api logout` function, or eval \"$(api logout --emit)\""}
			}
			rs, prof := a.resolve()
			sessionEnv := a.getenv("API_ENV")
			unset := []string{}
			if a.envName == "" || a.envName == sessionEnv {
				unset = append(unset, session.EnvToken)
			}
			envs := []string{}
			if rs.Name != "" {
				envs = append(envs, rs.Name)
			}
			if a.envName != "" { // an env that is no longer in the config can still have a cached token
				envs = append(envs, a.envName)
			}
			if prof.TokenEnv != "" {
				unset = append(unset, prof.TokenEnv)
			}
			if all {
				for _, n := range a.cfg.EnvNames() {
					envs = append(envs, n)
					if e := a.cfg.Env[n]; e.TokenEnv != "" && e.TokenEnv != prof.TokenEnv {
						unset = append(unset, e.TokenEnv)
					}
				}
			}
			forgot := map[string]bool{}
			for _, e := range envs {
				if !forgot[e] && a.forgetCachedToken(e) {
					forgot[e] = true
				}
			}
			msg := "✓ logged out"
			if len(forgot) > 0 {
				msg += fmt.Sprintf(" (cached token removed for %d env(s))", len(forgot))
			}
			a.say(msg)
			if len(unset) > 0 {
				fmt.Fprintln(a.io.Out, session.Unset(unset...))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&emit, "emit", false, "print shell statements for eval")
	cmd.Flags().BoolVar(&all, "all", false, "forget the cached token of every env in the config")
	return cmd
}
