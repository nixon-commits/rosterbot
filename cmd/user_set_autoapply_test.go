package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

type memUsers struct {
	users map[lineupapi.UserID]*lineupapi.User
	puts  []lineupapi.User
	err   error
}

func (m *memUsers) GetUser(_ context.Context, uid lineupapi.UserID) (*lineupapi.User, bool, error) {
	if m.err != nil {
		return nil, false, m.err
	}
	u, ok := m.users[uid]
	if !ok {
		return nil, false, nil
	}
	cp := *u
	return &cp, true, nil
}

func (m *memUsers) PutUser(_ context.Context, u *lineupapi.User) error {
	if m.err != nil {
		return m.err
	}
	m.puts = append(m.puts, *u)
	if m.users == nil {
		m.users = map[lineupapi.UserID]*lineupapi.User{}
	}
	cp := *u
	m.users[u.ID] = &cp
	return nil
}

func usersWith(u lineupapi.User) *memUsers {
	return &memUsers{users: map[lineupapi.UserID]*lineupapi.User{u.ID: &u}}
}

// TestSetAutoApply_TurnsTheWriteSwitchOn is the whole point of the command.
//
// auto_apply is the only field that decides whether the bot writes to a
// roster, and every creation path sets it false on purpose — "turned on by a
// person, not inherited from a migration". This is that person's tool. Without
// it the flag was unreachable outside hand-editing DynamoDB, which is why it
// went unenforced: there was no way to turn it on, so nothing could depend on
// it being meaningful.
func TestSetAutoApply_TurnsTheWriteSwitchOn(t *testing.T) {
	users := usersWith(lineupapi.User{ID: "alice", Status: lineupapi.UserActive, AutoApply: false})

	if err := setAutoApply(context.Background(), users, "alice", true); err != nil {
		t.Fatalf("setAutoApply: %v", err)
	}
	if len(users.puts) != 1 {
		t.Fatalf("wrote %d records, want 1", len(users.puts))
	}
	if !users.puts[0].AutoApply {
		t.Errorf("AutoApply = false after enabling")
	}
}

// TestSetAutoApply_TurnsItOffAgain: revoking consent must be as reachable as
// granting it. A tenant who asks the bot to stop touching their roster has to
// be answerable without a database edit.
func TestSetAutoApply_TurnsItOffAgain(t *testing.T) {
	users := usersWith(lineupapi.User{ID: "alice", Status: lineupapi.UserActive, AutoApply: true})

	if err := setAutoApply(context.Background(), users, "alice", false); err != nil {
		t.Fatalf("setAutoApply: %v", err)
	}
	if users.puts[0].AutoApply {
		t.Errorf("AutoApply = true after disabling")
	}
}

// TestSetAutoApply_PreservesEveryOtherField. This writes a whole record back,
// so a field dropped here is a field silently erased — the team binding that
// connect proved, the attested email, the role. Read-modify-write, never
// construct-and-overwrite.
func TestSetAutoApply_PreservesEveryOtherField(t *testing.T) {
	users := usersWith(lineupapi.User{
		ID:          "alice",
		DisplayName: "Alice",
		Email:       "alice@example.test",
		Role:        lineupapi.RoleAdmin,
		Status:      lineupapi.UserActive,
		TeamID:      "team-7",
	})

	if err := setAutoApply(context.Background(), users, "alice", true); err != nil {
		t.Fatalf("setAutoApply: %v", err)
	}

	got := users.puts[0]
	if got.DisplayName != "Alice" || got.Email != "alice@example.test" ||
		got.Role != lineupapi.RoleAdmin || got.Status != lineupapi.UserActive || got.TeamID != "team-7" {
		t.Errorf("record lost fields: %+v", got)
	}
}

// TestSetAutoApply_RefusesAnUnknownUser. Creating one here would mint a profile
// with no role, no status and no team that nonetheless has the write switch
// ON — the worst possible record to bring into existence by typo.
func TestSetAutoApply_RefusesAnUnknownUser(t *testing.T) {
	users := &memUsers{}

	if err := setAutoApply(context.Background(), users, "nobody", true); err == nil {
		t.Fatal("expected a refusal for an unknown user")
	}
	if len(users.puts) != 0 {
		t.Errorf("wrote %d records for an unknown user", len(users.puts))
	}
}

func TestSetAutoApply_RefusesAnEmptyUser(t *testing.T) {
	users := &memUsers{}
	if err := setAutoApply(context.Background(), users, "", true); err == nil {
		t.Fatal("expected a refusal for an empty user id")
	}
}

func TestSetAutoApply_ReportsAStoreOutage(t *testing.T) {
	users := &memUsers{err: errors.New("dynamo down")}
	if err := setAutoApply(context.Background(), users, "alice", true); err == nil {
		t.Fatal("expected the store error to surface")
	}
}
