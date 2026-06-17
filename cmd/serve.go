package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
	"github.com/spf13/cobra"
)

var (
	serveAddr string
	serveDir  string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the read-only lineup HTTP API locally (GET /v1/lineup/today)",
	Long: `Serve the read-only lineup API over HTTP for local testing before deploy.

It reads the precomputed JSON written by ` + "`optimize --publish-lineup`" + ` (the
same bytes the deployed Lambda serves) — it does NOT run the optimizer or touch
Fantrax. Requires ROSTERBOT_API_TOKEN; requests need an "Authorization: Bearer
<token>" header.

Typical local flow:
  go run . optimize --dry-run --publish-lineup   # writes .lineup/lineup-today.json
  ROSTERBOT_API_TOKEN=test go run . serve &
  curl -H "Authorization: Bearer test" localhost:8080/v1/lineup/today`,
	RunE: runServe,
}

func init() {
	serveCmd.Flags().StringVar(&serveAddr, "addr", ":8080", "address to listen on")
	serveCmd.Flags().StringVar(&serveDir, "dir", ".lineup", "directory holding published lineup JSON")
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
	token := os.Getenv("ROSTERBOT_API_TOKEN")
	if token == "" {
		return fmt.Errorf("ROSTERBOT_API_TOKEN is not set — the server needs a bearer token to authenticate requests")
	}
	h := lineupapi.Handler(lineupapi.Config{
		Token:         token,
		Lineups:       lineupapi.NewFileStore(serveDir),
		Runs:          lineupapi.NewFileRunStore(serveDir + "/runs"),
		Notifications: lineupapi.NewFileNotificationStore(serveDir + "/notifications"),
		// Jobs is nil locally: triggering real ECS tasks only makes sense on AWS.
		// POST /v1/jobs/* returns 501 from `serve`.
	})
	fmt.Printf("serving lineup API on %s (reading %s; jobs disabled locally)\n", serveAddr, serveDir)
	return http.ListenAndServe(serveAddr, h)
}
