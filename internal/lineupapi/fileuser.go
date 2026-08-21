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
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
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
	if err := os.Remove(s.credMetaPath(id, credID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// credMetaPath keeps a passkey's meta beside its credential — under the user's
// own directory, so DeleteUser's RemoveAll sweeps it — but in a separate file,
// so a rename and the login ceremony's sign-counter overwrite touch different
// paths and cannot contend.
func (s *FileUserStore) credMetaPath(id UserID, credID []byte) string {
	return filepath.Join(s.userDir(id), "credmeta", credFileName(credID))
}

func (s *FileUserStore) PutCredentialMeta(_ context.Context, id UserID, credID []byte, meta CredentialMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writeAtomic(s.credMetaPath(id, credID), data)
}

func (s *FileUserStore) CredentialMetas(_ context.Context, id UserID) (map[string]CredentialMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.userDir(id), "credmeta")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]CredentialMeta{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]CredentialMeta{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var m CredentialMeta
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		// The filename is credFileName(credID) = <b64>.json, so the map key is
		// the name minus its extension — the same CredentialKey encoding.
		out[strings.TrimSuffix(e.Name(), ".json")] = m
	}
	return out, nil
}

func (s *FileUserStore) DeleteUser(_ context.Context, id UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, _, ok, err := s.readProfile(id)
	if err != nil {
		return err
	}

	// Indexes first, like DeleteCredential: a credential that still resolves
	// is usable, and a crash mid-delete must leave the passkey dead rather
	// than the profile gone with a live login.
	if entries, err := os.ReadDir(s.credsDir(id)); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := os.Remove(filepath.Join(s.dir, "credindex", e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// Push token index entries, for the same reason as the credential index:
	// they live outside the user tree (they answer a cross-user question), so
	// the RemoveAll below cannot reach them. Guarded on the holder like the
	// claims release — an index mid-steal must never lose another user's entry.
	if devices, err := s.readPushDevices(id); err == nil {
		for _, d := range devices {
			owner, has, err := s.readPushTokenOwner(d.Token)
			if err != nil {
				return err
			}
			if has && owner.UserID == id {
				if err := os.Remove(s.pushTokenIndexPath(d.Token)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if err := os.RemoveAll(filepath.Dir(s.profilePath(id))); err != nil {
		return err
	}

	// Release the uniqueness claims this user held. Checked against the
	// holder so a half-written state can never release somebody else's claim.
	if ok {
		for _, c := range []struct{ kind, val string }{{"email", u.Email}, {"team", u.TeamID}} {
			if c.val == "" {
				continue
			}
			holder, has, err := s.claimHolder(c.kind, c.val)
			if err != nil {
				return err
			}
			if has && holder == id {
				if err := os.Remove(s.claimPath(c.kind, c.val)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
		}
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

// --- PushDeviceStore ---------------------------------------------------------
//
// Devices live under the user's own directory so DeleteUser's RemoveAll sweeps
// them with the account:
//
//	<dir>/users/<b64url(uid)>/push/<b64url(deviceID)>.json
//
// The token index lives OUTSIDE the user tree, beside credindex, because it
// answers a cross-user question: "who currently holds this APNs token?" That
// is what makes registration able to steal a token from its previous owner
// (see PushDeviceStore), which a per-user layout cannot express.
//
//	<dir>/pushtokens/<b64url(token)>  ->  {user_id, device_id}

func (s *FileUserStore) pushDir(id UserID) string {
	return filepath.Join(s.userDir(id), "push")
}

// pushDeviceFileName encodes the id for the same reason userDir encodes the
// user id: the id in DeletePushDevice arrives from a URL path value, and
// encoding removes the path-injection question instead of answering it per
// caller (see userDir).
func pushDeviceFileName(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id)) + ".json"
}

func (s *FileUserStore) pushTokenIndexPath(token string) string {
	return filepath.Join(s.dir, "pushtokens", base64.RawURLEncoding.EncodeToString([]byte(token)))
}

// pushTokenOwner is the token index entry: which user's which device holds a
// token. Unlike credindex (bare uid bytes) it needs both halves, because the
// steal has to delete one specific device file.
type pushTokenOwner struct {
	UserID   UserID `json:"user_id"`
	DeviceID string `json:"device_id"`
}

func (s *FileUserStore) readPushTokenOwner(token string) (pushTokenOwner, bool, error) {
	b, err := os.ReadFile(s.pushTokenIndexPath(token))
	if errors.Is(err, fs.ErrNotExist) {
		return pushTokenOwner{}, false, nil
	}
	if err != nil {
		return pushTokenOwner{}, false, err
	}
	var o pushTokenOwner
	if err := json.Unmarshal(b, &o); err != nil {
		return pushTokenOwner{}, false, err
	}
	return o, true, nil
}

// readPushDevices is the lock-free half of PushDevices, callable by methods
// already holding s.mu.
func (s *FileUserStore) readPushDevices(id UserID) ([]PushDevice, error) {
	entries, err := os.ReadDir(s.pushDir(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []PushDevice{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.pushDir(id), e.Name()))
		if err != nil {
			return nil, err
		}
		var d PushDevice
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *FileUserStore) PutPushDevice(_ context.Context, uid UserID, d PushDevice) (PushDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The steal (see PushDeviceStore): another user holding this token loses
	// their record before ours is written. Deleting the file and not the index
	// is deliberate — the index is about to be overwritten below, and an
	// intermediate remove would only widen the crash window.
	if owner, ok, err := s.readPushTokenOwner(d.Token); err != nil {
		return PushDevice{}, err
	} else if ok && owner.UserID != uid {
		stale := filepath.Join(s.pushDir(owner.UserID), pushDeviceFileName(owner.DeviceID))
		if err := os.Remove(stale); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return PushDevice{}, err
		}
	}

	existing, err := s.readPushDevices(uid)
	if err != nil {
		return PushDevice{}, err
	}
	for _, e := range existing {
		if e.Token != d.Token {
			continue
		}
		if d.ID == "" {
			// Update in place. CreatedAt is the device's first sighting and
			// must survive; LastSeenAt is what the new registration advances.
			d.ID, d.CreatedAt = e.ID, e.CreatedAt
			continue
		}
		// A SECOND row with the same token is a lost race between two
		// concurrent registrations (both read "no match", both minted an id).
		// Left alone it delivers every notification twice, forever — the
		// duplicate is a live token, so ErrDeviceGone never prunes it. Healing
		// here makes the damage one launch long: the client re-registers on
		// every launch, and this pass collapses the duplicates back to one.
		if err := os.Remove(filepath.Join(s.pushDir(uid), pushDeviceFileName(e.ID))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return PushDevice{}, err
		}
	}
	if d.ID == "" {
		d.ID = NewPushDeviceID()
	}

	data, err := json.Marshal(d)
	if err != nil {
		return PushDevice{}, err
	}
	if err := writeAtomic(filepath.Join(s.pushDir(uid), pushDeviceFileName(d.ID)), data); err != nil {
		return PushDevice{}, err
	}
	// Index last, so it never names a record that does not exist. A crash
	// before this line leaves the token unindexed, which the next registration
	// repairs; the reverse order could leave an index pointing at nothing.
	idx, err := json.Marshal(pushTokenOwner{UserID: uid, DeviceID: d.ID})
	if err != nil {
		return PushDevice{}, err
	}
	if err := writeAtomic(s.pushTokenIndexPath(d.Token), idx); err != nil {
		return PushDevice{}, err
	}
	return d, nil
}

func (s *FileUserStore) PushDevices(_ context.Context, id UserID) ([]PushDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readPushDevices(id)
}

func (s *FileUserStore) DeletePushDevice(_ context.Context, uid UserID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.pushDir(uid), pushDeviceFileName(id))
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // deleting an absent device is a success, not an error
	}
	if err != nil {
		return err
	}
	var d PushDevice
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}

	// Device file FIRST, index second — the opposite of DeleteCredential's
	// order, because the risk points the other way. A push device's file is
	// what delivery reads; the index only routes the steal. A crash after the
	// file remove leaves a stale index that the next registration repairs,
	// while the reverse order would leave a device that keeps receiving
	// notifications with no index left to steal it through.
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if owner, ok, err := s.readPushTokenOwner(d.Token); err != nil {
		return err
	} else if ok && owner.UserID == uid && owner.DeviceID == id {
		if err := os.Remove(s.pushTokenIndexPath(d.Token)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
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
