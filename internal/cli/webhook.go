package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/apply"
	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/token"
)

func newWebhook(g *globals) *cobra.Command {
	var runtime, hookURL string
	cmd := &cobra.Command{
		Use:   "webhook <factory> <stub>",
		Short: "Give Fabrica the webhook a runtime made for a pace routine",
		Long: `A routine with a pace trigger runs on its factory's heartbeat: the
Fabrica server beats once every pace, and calls the routine's webhook on
the beats it runs on. Each runtime makes the webhook when its stub is
saved (an API trigger on a Claude routine, a webhook trigger on a Cursor
automation), but no runtime's API hands its URL and token over, so you
copy them from the runtime and give them to Fabrica with this command.

<stub> is the stub's identity, like '4ckr#tidy@github.com/acme/app'.
Handoffs, Cursor's setup file and ` + "`fabrica plan`" + ` print the exact command
for each stub that needs it. Pass the webhook's URL with --url, and paste
its token (or API key) when asked; without a terminal, it's read from
stdin.

The token is sealed with the factory's server secret (from
FABRICA_SERVER_SECRET, or saved by ` + "`fabrica auth login`" + `) and kept in the
factory's state file, so only the Fabrica server can read it. Like confirm,
it commits and pushes the state file, and refuses while an apply holds the
factory's lock. Updates keep the webhook; after rotating the server
secret, run this again for each one (` + "`fabrica plan`" + ` lists them).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dir, err := g.homeDir(ctx)
			if err != nil {
				return err
			}
			h, err := home.Open(dir)
			if err != nil {
				return err
			}
			factory, identity := args[0], args[1]
			d := h.Factories[factory]
			switch {
			case d == nil:
				return fmt.Errorf("there's no factory %s in %s", factory, dir)
			case d.Scope != provider.Cloud:
				return fmt.Errorf("factory %s is local, and only cloud factories have a Fabrica server to call webhooks", factory)
			case runtime == "":
				return errors.New("--runtime is required: the runtime the stub is on, like claude-code-cloud or cursor-cloud")
			}
			secret, err := webhookSecret(d.Server)
			if err != nil {
				return err
			}
			w := apply.Webhook{Factory: factory, Runtime: runtime, Identity: identity, URL: hookURL, Secret: secret}
			if w.Token, err = newPrompter(cmd.InOrStdin(), cmd.ErrOrStderr()).secret("The webhook's token (or API key): "); err != nil {
				return fmt.Errorf("couldn't read the webhook's token: %w", err)
			}
			res, err := apply.SetWebhook(ctx, dir, w, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Recorded the webhook of %s @ %s on %s (factory %s). The Fabrica server calls it on the factory's pace, from its next refresh.\n", res.Routine, res.Repo, res.Runtime, factory)
			return nil
		},
	}
	cmd.Flags().StringVar(&runtime, "runtime", "", "the runtime the stub is on, like claude-code-cloud or cursor-cloud")
	cmd.Flags().StringVar(&hookURL, "url", "", "the webhook's URL, as the runtime shows it")
	return cmd
}

// webhookSecret finds the server secret that seals a factory's webhook
// tokens: FABRICA_SERVER_SECRET, or else the one saved for its server.
func webhookSecret(server string) (string, error) {
	if os.Getenv(token.EnvVar) != "" {
		return token.SecretFromEnv()
	}
	return savedServerSecret(server)
}
