package lineupapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// FileUserStore is a local-filesystem UserStore for `rosterbot serve`.
//
//	<dir>/users/<b64url(uid)>/profile.json
//	<dir>/users/<b64url(uid)>/creds/<b64url(credID)>.json
//
// The layout mirrors the DynamoDB key design deliberately — one file per item,
// credentials as siblings of the profile rather than inside it — so the two
// implementations fail the conformance suite in the same places rather than
// papering over a difference.
//
// Concurrency is a process-wide mutex plus atomic rename. That is honestly
// scoped: a single-operator dev server has one process, and the file system
// offers no compare-and-swap to build on. The backend that actually runs
// concurrently is DynamoDB, where the precondition is enforced server-side.
type FileUserStore struct {
	mu  sync.Mutex
	dir string
}

func NewFileUserStore(dir string) *FileUserStore { return &FileUserStore{dir: dir} }

var _ UserStore = (*FileUserStore)(nil)

// userDir is the one place a user id becomes a path, so it is the one place
// that has to make a path safe.
//
// THE ID IS ENCODED, NEVER INTERPOLATED. filepath.Join calls Clean, so a "../"
// inside an id does not stay a literal directory name — it escapes. Every path
// in this store funnels through here, which meant the profile read, the profile
// WRITE and the credential listing were all affected: a read leaks somebody
// else's file, a write destroys one. (CodeQL go/path-injection #16, reported on
// readProfile; readProfile was simply where the scanner happened to land.)
//
// A real UserID is base64url — NewUserID encodes the WebAuthn handle — so no id
// this store's own callers construct contains a dot or a slash, and nothing
// exploitable exists today. The exposure is every id that arrives from outside
// that constructor: the --user admin flags, a hand-edited store, the next
// caller nobody has written yet. Encoding removes the question rather than
// answering it for the current callers only.
//
// base64url is the same tool claimPath below already reaches for, and its
// alphabet ([A-Za-z0-9_-]) provably contains no path metacharacter. Validation
// would preserve prettier directory names, but it would have to return an error
// that four call sites do not currently have anywhere to put.
//
// The cost is that ids are double-encoded on disk, so directory names are no
// longer eyeball-matchable to a UserID, and any store written before this is
// orphaned. Both are acceptable: this backend exists for `rosterbot serve`, and
// production runs on DynamoDB.
func (s *FileUserStore) userDir(id UserID) string {
	return filepath.Join(s.dir, "users", base64.RawURLEncoding.EncodeToString([]byte(id)))
}
func (s *FileUserStore) profilePath(id UserID) string {
	return filepath.Join(s.userDir(id), "profile.json")
}
func (s *FileUserStore) credsDir(id UserID) string {
	return filepath.Join(s.userDir(id), "creds")
}
func credFileName(credID []byte) string {
	return base64.RawURLEncoding.EncodeToString(credID) + ".json"
}

// versionOf derives the optimistic-concurrency token from the stored bytes.
// A content hash rather than a counter: the token is opaque by contract, and a
// counter kept inside the body would be self-referential (it would change the
// bytes it describes). Any change to the file changes the version, which is
// exactly what the precondition needs.
func versionOf(b []byte) IdentityVersion {
	sum := sha256.Sum256(b)
	return IdentityVersion(hex.EncodeToString(sum[:]))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readProfile returns the stored user and the version its bytes hash to.
func (s *FileUserStore) readProfile(id UserID) (*User, []byte, bool, error) {
	data, err := os.ReadFile(s.profilePath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, nil, false, err
	}
	u.Version = versionOf(data)
	return &u, data, true, nil
}

func (s *FileUserStore) GetUser(_ context.Context, id UserID) (*User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, _, ok, err := s.readProfile(id)
	return u, ok, err
}

// claimPath is the uniqueness index. A claim is a file whose NAME is the
// claimed value, so the filesystem itself enforces one holder — the same job
// the EMAIL#/TEAM# items do in DynamoDB.
func (s *FileUserStore) claimPath(kind, value string) string {
	return filepath.Join(s.dir, "claims", kind, base64.RawURLEncoding.EncodeToString([]byte(strings.ToLower(value))))
}

func (s *FileUserStore) claimHolder(kind, value string) (UserID, bool, error) {
	b, err := os.ReadFile(s.claimPath(kind, value))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return UserID(b), true, nil
}

func (s *FileUserStore) CreateUser(_ context.Context, u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, _, ok, err := s.readProfile(u.ID); err != nil {
		return err
	} else if ok {
		return ErrUserConflict
	}
	if u.Email != "" {
		if holder, ok, err := s.claimHolder("email", u.Email); err != nil {
			return err
		} else if ok && holder != u.ID {
			return ErrEmailTaken
		}
	}
	if u.TeamID != "" {
		if holder, ok, err := s.claimHolder("team", u.TeamID); err != nil {
			return err
		} else if ok && holder != u.ID {
			return ErrTeamTaken
		}
	}

	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.profilePath(u.ID), data); err != nil {
		return err
	}
	if u.Email != "" {
		if err := writeAtomic(s.claimPath("email", u.Email), []byte(u.ID)); err != nil {
			return err
		}
	}
	if u.TeamID != "" {
		if err := writeAtomic(s.claimPath("team", u.TeamID), []byte(u.ID)); err != nil {
			return err
		}
	}
	u.Version = versionOf(data)
	return nil
}

func (s *FileUserStore) PutUser(_ context.Context, u *User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, _, ok, err := s.readProfile(u.ID)
	if err != nil {
		return err
	}
	// An absent record and a moved one are the same failure to the caller: the
	// state they asserted is not the state on disk, and the recovery is a
	// re-read either way.
	if !ok || cur.Version != u.Version {
		return ErrUserConflict
	}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.profilePath(u.ID), data); err != nil {
		return err
	}
	u.Version = versionOf(data)
	return nil
}

func (s *FileUserStore) ClaimTeam(_ context.Context, id UserID, teamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if holder, ok, err := s.claimHolder("team", teamID); err != nil {
		return err
	} else if ok && holder != id {
		return ErrTeamTaken
	}
	u, _, ok, err := s.readProfile(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUserConflict
	}
	u.TeamID = teamID
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.profilePath(id), data); err != nil {
		return err
	}
	return writeAtomic(s.claimPath("team", teamID), []byte(id))
}

func (s *FileUserStore) Credentials(_ context.Context, id UserID) ([]webauthn.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.credsDir(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Sorted for determinism: the interface promises no order, but an
	// implementation whose order changes between reads makes every test that
	// touches it flaky for no reason.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	out := []webauthn.Credential{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.credsDir(id), e.Name()))
		if err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *FileUserStore) PutCredential(_ context.Context, id UserID, c webauthn.Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.credsDir(id), credFileName(c.ID)), data); err != nil {
		return err
	}
	// The reverse index for the non-discoverable fallback. Written after the
	// credential, so a crash between the two leaves a usable credential that is
	// merely unresolvable by id — recoverable. The other order would leave an
	// index entry pointing at nothing.
	return writeAtomic(s.credIndexPath(c.ID), []byte(id))
}

func (s *FileUserStore) credIndexPath(credID []byte) string {
	return filepath.Join(s.dir, "credindex", credFileName(credID))
}

func (s *FileUserStore) DeleteCredential(_ context.Context, id UserID, credID []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Drop the index first: a revoked credential that is still resolvable is
	// worse than one that is merely orphaned, because resolvable means usable.
	if err := os.Remove(s.credIndexPath(credID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(filepath.Join(s.credsDir(id), credFileName(credID))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FileUserStore) UserByCredential(_ context.Context, credID []byte) (UserID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	b, err := os.ReadFile(s.credIndexPath(credID))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return UserID(b), true, nil
}

func (s *FileUserStore) ListActive(ctx context.Context) ([]*User, error) {
	return s.listWhere(func(u *User) bool { return u.Status == UserActive })
}

func (s *FileUserStore) ListUsers(ctx context.Context) ([]*User, error) {
	return s.listWhere(func(*User) bool { return true })
}

func (s *FileUserStore) listWhere(keep func(*User) bool) ([]*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(filepath.Join(s.dir, "users"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []*User{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// The directory name is the ENCODED id (see userDir), so it has to be
		// decoded before it is an id again. Passing e.Name() straight through
		// would re-encode it on the way back into a path and look for a
		// directory that cannot exist — the listing silently returning nothing,
		// which for a fan-out means every tenant quietly skipped.
		raw, decErr := base64.RawURLEncoding.DecodeString(e.Name())
		if decErr != nil {
			// Not one of ours — a stray directory, or a store written before
			// the encoding. Skipping is right: this listing drives the fan-out,
			// and inventing a tenant from an unparseable name is worse than
			// omitting a directory that holds no profile anyway.
			continue
		}
		u, _, ok, err := s.readProfile(UserID(raw))
		if err != nil {
			return nil, err
		}
		if ok && keep(u) {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// --- EnrollmentStore ---------------------------------------------------------
//
// Enrollment links live under <dir>/enroll/<tokenHash>.json. The mutex that
// guards the rest of this store is what makes redemption atomic here; see
// ddbuser for the backend where atomicity has to be a server-side condition.

var _ EnrollmentStore = (*FileUserStore)(nil)

func (s *FileUserStore) enrollPath(tokenHash string) string {
	return filepath.Join(s.dir, "enroll", tokenHash+".json")
}

func (s *FileUserStore) readEnrollment(tokenHash string) (Enrollment, bool, error) {
	var e Enrollment
	b, err := os.ReadFile(s.enrollPath(tokenHash))
	if errors.Is(err, fs.ErrNotExist) {
		return e, false, nil
	}
	if err != nil {
		return e, false, err
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, false, err
	}
	return e, true, nil
}

func (s *FileUserStore) CreateEnrollment(_ context.Context, tokenHash string, e Enrollment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok, err := s.readEnrollment(tokenHash); err != nil {
		return err
	} else if ok {
		// Overwriting would reset a spent link back to unused, which is the one
		// outcome a single-use token must never have.
		return ErrUserConflict
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return writeAtomic(s.enrollPath(tokenHash), data)
}

func (s *FileUserStore) GetEnrollment(_ context.Context, tokenHash string) (Enrollment, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readEnrollment(tokenHash)
}

func (s *FileUserStore) RedeemEnrollment(_ context.Context, tokenHash string, now time.Time) (Enrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok, err := s.readEnrollment(tokenHash)
	if err != nil {
		return Enrollment{}, err
	}
	// Unknown, expired and already-redeemed collapse to one error on the way
	// out: distinguishing them would answer "does this token exist?" for anyone
	// who asks. Expiry is checked here rather than assumed cleaned up.
	if !ok || e.Redeemed() || e.Expired(now) {
		return Enrollment{}, ErrEnrollmentInvalid
	}
	e.UsedAt = now
	data, err := json.Marshal(e)
	if err != nil {
		return Enrollment{}, err
	}
	if err := writeAtomic(s.enrollPath(tokenHash), data); err != nil {
		return Enrollment{}, err
	}
	return e, nil
}
