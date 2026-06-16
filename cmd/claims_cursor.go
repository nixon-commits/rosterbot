package cmd

// resolveCursorPath returns the cursor path from the env override, or "" to let
// claims.Run apply its default (.cache/last-claims.json). On AWS the container
// sets CLAIMS_CURSOR_PATH=.waivers/last-claims.json so the cursor rides the
// single-writer claims/ S3 prefix instead of the shared cache/ prefix.
func resolveCursorPath(env string) string {
	return env
}
