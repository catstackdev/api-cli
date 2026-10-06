package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"cybercat/api/internal/secretstore"
	"cybercat/api/internal/ui"

	"github.com/spf13/cobra"
)

// storeErr turns store errors into messages that say what to do.
func storeErr(name string, err error) error {
	switch {
	case errors.Is(err, secretstore.ErrNotFound):
		return &ExitError{Code: exitGeneric, Msg: fmt.Sprintf("✗ nothing stored as %q: api secret set %s", name, name)}
	case errors.Is(err, secretstore.ErrUnavailable):
		return &ExitError{Code: exitGeneric, Msg: "✗ " + err.Error()}
	}
	return err
}

func describeBinding(origins []string) string {
	if len(origins) == 0 {
		return "not bound to a server: it can be sent anywhere you point it (restrict it: api secret bind NAME <url>)"
	}
	return "bound to " + strings.Join(origins, ", ")
}

func (a *app) secretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Store passwords in the system credential store (macOS Keychain)",
		Long: `Secrets live in the operating system's credential store, never in a file this
tool writes. Refer to one anywhere a value is accepted as @keychain:NAME:

  api secret set upscale-dev --for https://api.dev.upscale.technology
  api login --gql ... -V input.password=@keychain:upscale-dev

--for binds the secret to the server(s) it may be sent to, like a browser's
password manager: a repo's config, an alias or a pasted command cannot make it go
anywhere else. A project config may only use bound secrets.

Entries are namespaced (the Keychain service is "api:NAME"), so this tool cannot
read or overwrite another application's items.`,
	}

	var (
		stdin   bool
		forURLs []string
		unbind  bool
	)
	set := &cobra.Command{
		Use:   "set <name> [--for <url>]...",
		Short: "Store a secret (hidden prompt, or one line from stdin with --stdin)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := secretstore.ValidateName(name); err != nil {
				return err
			}
			origins, err := secretstore.NormalizeOrigins(forURLs)
			if err != nil {
				return err
			}
			// replacing a password must not silently drop its binding
			if len(origins) == 0 && !unbind {
				if old, err := a.store.Get(name); err == nil {
					origins = old.Origins
				}
			}
			var value string
			if stdin {
				line, err := bufio.NewReader(a.io.In).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("no secret on stdin")
				}
				value = strings.TrimRight(line, "\r\n")
			} else {
				v, err := a.readSecret("Secret for " + name)
				if err != nil {
					return err
				}
				value = v
			}
			if value == "" {
				return fmt.Errorf("empty secret: nothing stored")
			}
			if err := a.store.Set(name, secretstore.Secret{Value: value, Origins: origins}); err != nil {
				return storeErr(name, err)
			}
			_ = a.secretIndex().Add(name)
			a.say(fmt.Sprintf("✓ stored %q (%d characters), %s. Use it as @keychain:%s", name, len([]rune(value)), describeBinding(origins), name))
			return nil
		},
	}
	set.Flags().BoolVar(&stdin, "stdin", false, "read the secret from one line of stdin (for scripts; never from an argument)")
	set.Flags().StringArrayVar(&forURLs, "for", nil, "a server this secret may be sent to, e.g. https://api.dev.example.com (repeatable)")
	set.Flags().BoolVar(&unbind, "unbind", false, "replace the secret AND remove its server binding")

	bind := &cobra.Command{
		Use:   "bind <name> <url>...",
		Short: "Add servers an existing secret may be sent to (the value is not re-entered)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			sec, err := a.store.Get(name)
			if err != nil {
				return storeErr(name, err)
			}
			origins, err := secretstore.NormalizeOrigins(append(append([]string{}, sec.Origins...), args[1:]...))
			if err != nil {
				return err
			}
			sec.Origins = origins
			if err := a.store.Set(name, sec); err != nil {
				return storeErr(name, err)
			}
			_ = a.secretIndex().Add(name)
			a.say(fmt.Sprintf("✓ %q is %s", name, describeBinding(origins)))
			return nil
		},
	}

	var reveal bool
	show := &cobra.Command{
		Use:   "show <name>",
		Short: "Check that a secret exists and where it may go (its value is printed only with --reveal)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sec, err := a.store.Get(args[0])
			if err != nil {
				return storeErr(args[0], err)
			}
			_ = a.secretIndex().Add(args[0]) // adopt a secret stored before the index existed
			if reveal {
				fmt.Fprintln(a.io.Out, sec.Value)
				return nil
			}
			a.say(fmt.Sprintf("✓ %q is stored (%d characters), %s; --reveal prints it", args[0], len([]rune(sec.Value)), describeBinding(sec.Origins)))
			return nil
		},
	}
	show.Flags().BoolVar(&reveal, "reveal", false, "print the value to stdout")

	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Delete a stored secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.store.Delete(args[0]); err != nil {
				return storeErr(args[0], err)
			}
			_ = a.secretIndex().Remove(args[0])
			a.say("✓ removed " + args[0])
			return nil
		},
	}
	var prune bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List the secrets you stored (names and where they may go; never values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names := a.knownSecretNames()
			if len(names) == 0 {
				a.say("  (no secrets known: api secret set NAME --for <url>)")
				return nil
			}
			var rows [][]string
			var gone []string
			for _, n := range names {
				sec, err := a.store.Get(n)
				switch {
				case errors.Is(err, secretstore.ErrNotFound):
					rows = append(rows, []string{n, "missing", "gone from the credential store"})
					gone = append(gone, n)
				case err != nil:
					return storeErr(n, err)
				case len(sec.Origins) == 0:
					rows = append(rows, []string{n, "unbound", "any server (api secret bind " + n + " <url>)"})
				default:
					rows = append(rows, []string{n, "bound", strings.Join(sec.Origins, ", ")})
				}
			}
			ui.Table(a.io.Out, []string{"NAME", "STATE", "SENT TO"}, rows, a.colorOut())
			if prune {
				for _, n := range gone {
					_ = a.secretIndex().Remove(n)
				}
				if len(gone) > 0 {
					a.say(fmt.Sprintf("✓ forgot %d name(s) that are gone from the credential store", len(gone)))
				}
			}
			return nil
		},
	}
	list.Flags().BoolVar(&prune, "prune", false, "forget names that are gone from the credential store")

	names := func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var out []cobra.Completion
		for _, n := range a.knownSecretNames() {
			if strings.HasPrefix(n, toComplete) {
				out = append(out, n)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
	show.ValidArgsFunction, rm.ValidArgsFunction, bind.ValidArgsFunction = names, names, names
	set.ValidArgsFunction = names
	cmd.AddCommand(set, bind, show, rm, list)
	return cmd
}
