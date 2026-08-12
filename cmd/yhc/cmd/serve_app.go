package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/webui"
	"github.com/abietic/yhc/server/appserver"
)

var appServerEngineInitializationMu sync.Mutex

type serveAppOptions struct {
	runtime     runtimeFlags
	listen      string
	eventBuffer int
	maxSessions int
	web         bool
}

func newServeAppCommand() *cobra.Command {
	options := &serveAppOptions{}
	command := &cobra.Command{
		Use:   "app",
		Short: "Start the authenticated Desktop app-server",
		Long:  "Start the authenticated loopback app-server used by the YHC Desktop application.",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.runtime.captureExplicit(cmd)
			return runServeApp(cmd, *options)
		},
	}
	bindRuntimeFlags(command.Flags(), &options.runtime)
	command.Flags().StringVar(&options.listen, "listen", "127.0.0.1:0", "Explicit loopback listen address")
	command.Flags().IntVar(&options.eventBuffer, "event-buffer", 1024, "Per-session replay event capacity")
	command.Flags().IntVar(&options.maxSessions, "max-sessions", 32, "Maximum live Desktop sessions")
	command.Flags().BoolVar(&options.web, "web", false, "Serve the same-origin browser client")
	return command
}

func runServeApp(cmd *cobra.Command, options serveAppOptions) error {
	if options.eventBuffer <= 0 {
		return usageErrorf("--event-buffer must be positive")
	}
	if options.maxSessions <= 0 {
		return usageErrorf("--max-sessions must be positive")
	}
	listener, err := net.Listen("tcp", options.listen)
	if err != nil {
		return fmt.Errorf("listen for app-server: %w", err)
	}
	defer listener.Close() //nolint:errcheck
	workspaceCWD := mustCwd()
	catalogPath, _ := enginesession.DefaultCatalogPaths()

	factory := func(ctx context.Context, input appserver.EngineOptions) (appserver.SessionEngine, error) {
		appServerEngineInitializationMu.Lock()
		defer appServerEngineInitializationMu.Unlock()
		engineConfig, _, _, err := buildEngineConfigForCWD(ctx, options.runtime, input.CWD, cmd.ErrOrStderr())
		if err != nil {
			return nil, err
		}
		engineConfig.SessionID = input.SessionID
		engineConfig.ThreadID = input.ThreadID
		engineConfig.RootSessionID = input.SessionID
		engineConfig.TranscriptDir = input.TranscriptDir
		engineConfig.PermissionProjectRoot = input.CWD
		engineConfig.PermissionPrompt = input.PermissionPrompt
		engineConfig.RepeatedToolCallPrompt = input.RepeatedToolCallPrompt
		engineConfig.EnableLongSessionServices = true
		engineConfig.CommandEntrypoint = commands.EntrypointAppServer
		queryEngine := engine.NewQueryEngine(engineConfig)
		if input.Resume {
			if _, err := queryEngine.ResumeSession(ctx, input.SessionID); err != nil {
				queryEngine.Close()
				return nil, fmt.Errorf("resume Desktop session: %w", err)
			}
		}
		return queryEngine, nil
	}
	server, err := appserver.New(appserver.Config{
		Factory:            factory,
		EventBuffer:        options.eventBuffer,
		MaxSessions:        options.maxSessions,
		EnableWeb:          options.web,
		WebAssets:          appWebAssets(options.web),
		SessionCatalogPath: catalogPath,
		DiscoveryCWD:       workspaceCWD,
	})
	if err != nil {
		return err
	}
	bootstrap, err := server.BootstrapFor(listener)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(bootstrap); err != nil {
		return fmt.Errorf("write app-server bootstrap: %w", err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s app-server started at %s\n", identity.CommandName, bootstrap.URL)

	serverCtx, cancelServer := context.WithCancel(cmd.Context())
	defer cancelServer()
	shutdownDone := make(chan error, 1)
	go func() {
		<-serverCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(shutdownCtx)
	}()
	serveErr := server.Serve(listener)
	cancelServer()
	shutdownErr := <-shutdownDone
	if cmd.Context().Err() != nil {
		return nil
	}
	if serveErr != nil {
		return serveErr
	}
	return shutdownErr
}

func appWebAssets(enabled bool) fs.FS {
	if !enabled {
		return nil
	}
	return webui.Assets
}
