package cli

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/sonarr-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

const (
	mcpPath           = "/mcp"
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server (stdio by default, HTTP with --listen)",
		Long: `Runs the MCP server for AI clients such as Claude Code.

Without --listen it speaks MCP over stdio: register it in .mcp.json with the SONARR_*
environment variables set. With --listen (SONARR_LISTEN, e.g. :8080) it serves the
Streamable HTTP transport at /mcp instead, for an always-on deployment such as the
docker-compose.yml in this repo. --auth-token (SONARR_AUTH_TOKEN) is then required, and
clients must send "Authorization: Bearer <token>"; to serve with no token at all, which
lets anyone who can reach the port use every tool, say so with --allow-no-auth
(SONARR_ALLOW_NO_AUTH=true).`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			client, err := f.NewClient()
			if err != nil {
				return err
			}

			server := mcp.NewServer(&mcp.Implementation{
				Name:    "sonarr",
				Title:   "Sonarr Library Auditor",
				Version: version.Version,
			}, nil)

			registered, err := tools.RegisterAll(server, client, f.ToolOptions())
			if err != nil {
				return err
			}
			clog.Log.Infof("registered %d tools", len(registered))

			if f.Listen == "" {
				return server.Run(cmd.Context(), &mcp.StdioTransport{})
			}
			if err := checkAuth(f.AuthToken, f.AllowNoAuth); err != nil {
				return err
			}

			return serveHTTP(cmd.Context(), server, f.Listen, f.AuthToken)
		},
	}
}

// checkAuth is what stands between --listen and an open port: with no bearer
// token the server refuses to start unless the operator said, in so many
// words, that no auth is wanted. A blank SONARR_AUTH_TOKEN in a copied .env
// would otherwise come up serving every tool to the whole network with one
// WARN line.
func checkAuth(token string, allowNoAuth bool) error {
	if token != "" || allowNoAuth {
		return nil
	}

	return errors.New("--listen needs --auth-token (SONARR_AUTH_TOKEN); to serve with no token at all, pass --allow-no-auth (SONARR_ALLOW_NO_AUTH=true)")
}

// newMux builds the HTTP routes: the MCP endpoint behind the bearer check, and
// an unauthenticated health probe for a container or a load balancer. It is
// separate from serveHTTP so the routing and the auth can be tested without
// binding a port.
func newMux(server *mcp.Server, authToken string) *http.ServeMux {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, requireBearer(authToken, handler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	return mux
}

// serveHTTP serves the MCP server over Streamable HTTP at /mcp (plus GET /healthz for
// container health checks) until the context is cancelled or SIGINT/SIGTERM arrives,
// then drains in-flight requests.
func serveHTTP(ctx context.Context, server *mcp.Server, addr, authToken string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(server, authToken),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	if authToken == "" {
		clog.Log.Warnf("no auth token set (SONARR_AUTH_TOKEN): anyone who can reach %s can use every tool", addr)
	}

	clog.Log.Infof("serving MCP over HTTP on %s%s", addr, mcpPath)

	errCh := make(chan error, 1)

	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("listening on %s: %w", addr, err)
	case <-ctx.Done():
	}

	clog.Log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	return nil
}

// requireBearer rejects requests without a matching "Authorization: Bearer <token>" header.
// An empty token disables the check.
func requireBearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}

	want := []byte("Bearer " + token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sonarr-mcp"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}
