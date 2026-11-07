package cli

import (
	"github.com/spf13/cobra"

	"github.com/staltz/fabrica/internal/home"
	"github.com/staltz/fabrica/internal/plan"
)

func newShow(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "show [factory...]",
		Short: "Show how each factory is set up",
		Long: `Show describes every factory (or the ones you name) as your home repo
and its blueprints set it up, without comparing it to what's installed:

  - the blueprint commit it resolves to, and whether the factory follows
    a branch or is frozen
  - its endpoint and tag, for cloud factories
  - each runtime's apply mode, and how it handles each routine's
    triggers, connectors and side effects (native, emulated or
    unsupported), memory and model, with the stub's fingerprint
  - which runtime the init routine would start on, before it has run

It also shows any errors that stop a factory from loading. To see what
apply would change, run ` + "`fabrica plan`" + `.`,
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
			t, err := newTools()
			if err != nil {
				return err
			}
			env, err := planEnv(t, args)
			if err != nil {
				return err
			}
			env.Refresh = false // what's set up, not what's installed
			plan.Build(ctx, env, h).RenderShow(cmd.OutOrStdout())
			return nil
		},
	}
}
