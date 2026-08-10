package commands

// registerAdditionalCommands keeps non-workflow compatibility and extension
// controls in the compiled core. Prompt recipes are loaded from the bundled
// workflow pack by the engine-owned prompt-command generation.
func registerAdditionalCommands(r *Registry) {
	r.mustRegister(&Command{
		Name:        "reload-plugins",
		Description: "Reload bundled and configured prompt commands for the current session",
		Usage:       "/reload-plugins",
		legacyExecute: func(_ *CommandContext, _ string) (*CommandResult, error) {
			return &CommandResult{Action: ActionReload}, nil
		},
	})
}
