package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/spf13/cobra"
)

var (
	serveAddr   string
	serveDir    string
	serveWebDir string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the read-only lineup HTTP API locally (GET /v1/lineup/today)",
	Long: `Serve the read-only lineup API over HTTP for local testing before deploy.

It reads the precomputed JSON written by ` + "`optimize --publish-lineup`" + ` (the
same bytes the deployed Lambda serves) — it does NOT run the optimizer or touch
Fantrax. Requires ROSTERBOT_API_TOKEN; requests need an "Authorization: Bearer
<token>" header.

It also serves the dashboard's static files (web/dashboard by default) at "/",
same-origin with the API — the same split CloudFront does in production between
its default behavior (static files) and its "/v1/*" behavior (the Lambda API),
so the dashboard's relative "/v1/..." fetches behave identically in both places.

Typical local flow:
  go run . optimize --dry-run --publish-lineup   # writes .lineup/lineup-today.json
  ROSTERBOT_API_TOKEN=test go run . serve
  open http://localhost:8080/                    # dashboard, same-origin API calls
  curl -H "Authorization: Bearer test" localhost:8080/v1/lineup/today`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":8080", "address to listen on")
	serveCmd.Flags().StringVar(&serveDir, "dir", ".lineup", "directory holding published lineup JSON")
	serveCmd.Flags().StringVar(&serveWebDir, "web", "web/dashboard", "directory holding the dashboard's static files, served at / (empty to disable)")
	rootCmd.AddCommand(serveCmd)
}

// newServeMux builds the local dev router: "/v1/*" goes to the lineup API
// (bearer-token authenticated, the same handler the deployed Lambda uses), and
// everything else is served as static files from webDir — mirroring the
// CloudFront default-behavior/"/v1/*"-behavior split used in production. An
// empty or missing webDir disables static serving (unmatched paths 404),
// which keeps the pre-dashboard `serve` workflow (curl-only lineup testing)
// working unchanged.
func newServeMux(token, lineupDir, webDir string) http.Handler {
	apiHandler := lineupapi.Handler(lineupapi.Config{
		Token:         token,
		Lineups:       lineupapi.NewFileStore(lineupDir),
		Runs:          lineupapi.NewFileRunStore(lineupDir + "/runs"),
		Notifications: lineupapi.NewFileNotificationStore(lineupDir + "/notifications"),
		Output:        lineupapi.NewFileOutputStore(lineupDir + "/outputs"),
		// Jobs is nil locally: triggering real ECS tasks only makes sense on AWS.
		// POST /v1/jobs/* returns 501 from `serve`.
	})

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	if webDir != "" {
		if _, err := os.Stat(webDir); err == nil {
			mux.Handle("/", http.FileServer(http.Dir(webDir)))
		}
	}
	return mux
}

func runServe(cmd *cobra.Command, args []string) error {
	token := os.Getenv("ROSTERBOT_API_TOKEN")
	if token == "" {
		return fmt.Errorf("ROSTERBOT_API_TOKEN is not set — the server needs a bearer token to authenticate requests")
	}
	if _, err := os.Stat(serveWebDir); err != nil {
		fmt.Printf("serving lineup API on %s (reading %s; jobs disabled locally; dashboard not served: %s not found)\n", serveAddr, serveDir, serveWebDir)
	} else {
		fmt.Printf("serving lineup API + dashboard on %s (reading %s; jobs disabled locally)\n", serveAddr, serveDir)
	}
	return http.ListenAndServe(serveAddr, newServeMux(token, serveDir, serveWebDir))
}
