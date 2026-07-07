package fantrax

import (
	"testing"

	"github.com/pmurley/go-fantrax/auth_client"
)

func gsIntPtr(n int) *int { return &n }

func TestExtractGSLimit_Found(t *testing.T) {
	categories := []auth_client.CategoryLimit{
		{Category: "Innings Pitched (IP)", Total: 42, Min: nil, Max: nil},
		{Category: "Games Started - Pitching (GS)", Total: 15, Min: gsIntPtr(15), Max: gsIntPtr(19)},
	}
	limits := extractGSLimit(categories)
	if limits.Min == nil || *limits.Min != 15 {
		t.Errorf("expected min=15, got %v", limits.Min)
	}
	if limits.Max == nil || *limits.Max != 19 {
		t.Errorf("expected max=19, got %v", limits.Max)
	}
}

func TestExtractGSLimit_NotFound(t *testing.T) {
	categories := []auth_client.CategoryLimit{
		{Category: "Innings Pitched (IP)", Total: 42, Min: nil, Max: nil},
	}
	limits := extractGSLimit(categories)
	if limits.Min != nil || limits.Max != nil {
		t.Errorf("expected nil/nil when GS category absent, got min=%v max=%v", limits.Min, limits.Max)
	}
}

func TestExtractGSLimit_NoLimitConfigured(t *testing.T) {
	categories := []auth_client.CategoryLimit{
		{Category: "Games Started - Pitching (GS)", Total: 15, Min: nil, Max: nil},
	}
	limits := extractGSLimit(categories)
	if limits.Min != nil || limits.Max != nil {
		t.Errorf("expected nil/nil when no min/max configured, got min=%v max=%v", limits.Min, limits.Max)
	}
}
