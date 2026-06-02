package cmd

import "testing"

func TestVerboseFlagExists(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("verbose")
	if f == nil {
		t.Fatal("expected --verbose persistent flag on root command")
	}
	if f.DefValue != "false" {
		t.Fatalf("expected default false, got %s", f.DefValue)
	}
}
