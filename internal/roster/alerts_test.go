package roster

import (
	"testing"

	"github.com/nixon-commits/fantrax-optimizer/internal/fantrax"
)

func TestCheckRoster_HealthyInIL(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Healthy Guy", Status: "Injured Reserve", IsInjured: false},
	}
	alerts := CheckRoster(players)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != HealthyInIL {
		t.Errorf("expected type %s, got %s", HealthyInIL, alerts[0].Type)
	}
}

func TestCheckRoster_CalledUpInMinors(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Called Up", Status: "Minors", InMinors: false},
	}
	alerts := CheckRoster(players)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != CalledUpInMinors {
		t.Errorf("expected type %s, got %s", CalledUpInMinors, alerts[0].Type)
	}
}

func TestCheckRoster_InjuredInActive(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Hurt Player", Status: "Active", IsInjured: true},
	}
	alerts := CheckRoster(players)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != InjuredInActive {
		t.Errorf("expected type %s, got %s", InjuredInActive, alerts[0].Type)
	}
}

func TestCheckRoster_MinorInActive(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Minor Leaguer", Status: "Reserve", InMinors: true},
	}
	alerts := CheckRoster(players)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != MinorInActive {
		t.Errorf("expected type %s, got %s", MinorInActive, alerts[0].Type)
	}
}

func TestCheckRoster_NoAlerts(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Active Healthy", Status: "Active", IsInjured: false, InMinors: false},
		{ID: "p2", Name: "Reserve Healthy", Status: "Reserve", IsInjured: false, InMinors: false},
		{ID: "p3", Name: "IL Injured", Status: "Injured Reserve", IsInjured: true},
		{ID: "p4", Name: "Minors Player", Status: "Minors", InMinors: true},
	}
	alerts := CheckRoster(players)
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
}

func TestCheckRoster_MultipleAlerts(t *testing.T) {
	players := []fantrax.Player{
		{ID: "p1", Name: "Healthy in IL", Status: "Injured Reserve", IsInjured: false},
		{ID: "p2", Name: "Called Up", Status: "Minors", InMinors: false},
		{ID: "p3", Name: "Hurt Active", Status: "Active", IsInjured: true},
		{ID: "p4", Name: "Minor on Reserve", Status: "Reserve", InMinors: true},
		{ID: "p5", Name: "Clean Player", Status: "Active", IsInjured: false, InMinors: false},
	}
	alerts := CheckRoster(players)
	if len(alerts) != 4 {
		t.Fatalf("expected 4 alerts, got %d", len(alerts))
	}

	types := make(map[AlertType]bool)
	for _, a := range alerts {
		types[a.Type] = true
	}
	for _, want := range []AlertType{HealthyInIL, CalledUpInMinors, InjuredInActive, MinorInActive} {
		if !types[want] {
			t.Errorf("missing alert type %s", want)
		}
	}
}
