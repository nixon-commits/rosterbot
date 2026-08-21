// Package ddbuser is the DynamoDB implementation of lineupapi.UserStore.
//
// It is its own package for the same reason s3lineup is: internal/lineupapi is
// imported by the API handler and by `serve`, and keeping the AWS SDK behind a
// sub-package means neither drags it in. The composition roots pick an
// implementation; nothing below them knows which one they picked.
//
// # Key layout
//
//	USER#<uid>    PROFILE          the profile item, carrying `ver`
//	USER#<uid>    CRED#<b64 id>    one item per passkey
//	CRED#<b64 id> USER             -> uid, the non-discoverable lookup
//	EMAIL#<lower> USER             -> uid, uniqueness
//	TEAM#<id>     USER             -> uid, uniqueness
//
// Credentials are separate items rather than a list on the profile, which is
// what makes a registration and a concurrent sign-counter update unable to
// contend: they write different sort keys. See lineupapi.UserStore.
package ddbuser

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// API is the slice of the DynamoDB client this package uses. Narrow on purpose:
// it is the seam the in-memory double implements, and every method here is one
// the store actually calls.
type API interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

// Store implements lineupapi.UserStore over one DynamoDB table.
type Store struct {
	api   API
	table string
}

var _ lineupapi.UserStore = (*Store)(nil)

// New builds a Store against the ambient AWS config.
func New(ctx context.Context, table string) (*Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return NewWithAPI(dynamodb.NewFromConfig(cfg), table), nil
}

// NewWithAPI builds a Store over an explicit client. Tests inject a double.
func NewWithAPI(api API, table string) *Store { return &Store{api: api, table: table} }

// Key helpers. Every key is built here so a layout change lands in one place.
func userPK(id lineupapi.UserID) string { return "USER#" + string(id) }
func credSK(credID []byte) string       { return "CRED#" + b64(credID) }
func credPK(credID []byte) string       { return "CRED#" + b64(credID) }
func emailPK(email string) string       { return "EMAIL#" + strings.ToLower(email) }
func teamPK(teamID string) string       { return "TEAM#" + teamID }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

// strAttr reads a string attribute, "" when absent or not a string.
func strAttr(v types.AttributeValue) string {
	sv, _ := v.(*types.AttributeValueMemberS)
	if sv == nil {
		return ""
	}
	return sv.Value
}
func n(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

const profileSK = "PROFILE"
const pointerSK = "USER"

// versionOf reads the profile item's monotonic `ver` attribute as the opaque
// token lineupapi.IdentityVersion promises. A counter is honest here, unlike on
// S3 where the real precondition has to be the ETag: DynamoDB conditions can
// compare an attribute directly, so the number IS the mechanism rather than a
// decoration beside it.
func versionOf(item map[string]types.AttributeValue) lineupapi.IdentityVersion {
	if v, ok := item["ver"].(*types.AttributeValueMemberN); ok {
		return lineupapi.IdentityVersion(v.Value)
	}
	return ""
}

func (s *Store) key(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: pk}, "sk": &types.AttributeValueMemberS{Value: sk}}
}

func (st *Store) GetUser(ctx context.Context, id lineupapi.UserID) (*lineupapi.User, bool, error) {
	out, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table),
		Key:       st.key(userPK(id), profileSK),
	})
	if err != nil {
		return nil, false, err
	}
	if len(out.Item) == 0 {
		return nil, false, nil
	}
	u, err := unmarshalUser(out.Item)
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

func unmarshalUser(item map[string]types.AttributeValue) (*lineupapi.User, error) {
	var u lineupapi.User
	if err := attributevalue.UnmarshalMap(item, &u); err != nil {
		return nil, err
	}
	u.Version = versionOf(item)
	return &u, nil
}

// attrStatus is the item attribute holding User.Status.
//
// attributevalue keys on the Go FIELD NAME — it honours `dynamodbav` tags and
// ignores `json` ones — so this is "Status", not "status". Naming it here with
// a test that pins it (TestScanFilterMatchesTheMarshalledAttribute) is the
// point: a filter naming an attribute that does not exist matches nothing, and
// a ListActive that quietly returns zero users means the fan-out skips every
// tenant with nothing failing anywhere to say so.
const attrStatus = "Status"

// attrVersion is the marshalled copy of User.Version, which must NOT reach the
// stored item. `json:"-"` does not stop attributevalue, and a persisted copy
// would be a stale duplicate shadowing `ver` — the real precondition attribute
// — which is precisely what identity.go's Version doc warns against.
const attrVersion = "Version"

func (st *Store) userItem(u *lineupapi.User, ver int64) (map[string]types.AttributeValue, error) {
	item, err := attributevalue.MarshalMap(u)
	if err != nil {
		return nil, err
	}
	delete(item, attrVersion)
	item["pk"] = s(userPK(u.ID))
	item["sk"] = s(profileSK)
	item["ver"] = n(ver)
	return item, nil
}

// CreateUser writes the profile and both uniqueness claims in ONE transaction.
//
// The claims cannot be checked first and written second: between the check and
// the write another request can take the same email or team, which is precisely
// the race these constraints exist to prevent. TransactWriteItems makes the
// three conditions and the three writes a single atomic decision.
func (st *Store) CreateUser(ctx context.Context, u *lineupapi.User) error {
	item, err := st.userItem(u, 1)
	if err != nil {
		return err
	}

	// Order matters only because it maps cancellation reasons back to errors.
	items := []types.TransactWriteItem{{
		Put: &types.Put{
			TableName:           aws.String(st.table),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		},
	}}
	kinds := []error{lineupapi.ErrUserConflict}

	if u.Email != "" {
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName:           aws.String(st.table),
			Item:                map[string]types.AttributeValue{"pk": s(emailPK(u.Email)), "sk": s(pointerSK), "uid": s(string(u.ID))},
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		}})
		kinds = append(kinds, lineupapi.ErrEmailTaken)
	}
	if u.TeamID != "" {
		items = append(items, types.TransactWriteItem{Put: &types.Put{
			TableName:           aws.String(st.table),
			Item:                map[string]types.AttributeValue{"pk": s(teamPK(u.TeamID)), "sk": s(pointerSK), "uid": s(string(u.ID))},
			ConditionExpression: aws.String("attribute_not_exists(pk)"),
		}})
		kinds = append(kinds, lineupapi.ErrTeamTaken)
	}

	if _, err := st.api.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return mapCancellation(err, kinds)
	}
	u.Version = "1"
	return nil
}

// mapCancellation turns a cancelled transaction into the specific error for
// whichever condition failed. Without this every uniqueness violation would
// surface as one indistinguishable "transaction cancelled", and the connect
// flow could not tell a user "that team is already claimed" from "that email
// is".
func mapCancellation(err error, kinds []error) error {
	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) {
		return err
	}
	for i, r := range canceled.CancellationReasons {
		if r.Code != nil && *r.Code == "ConditionalCheckFailed" && i < len(kinds) {
			return kinds[i]
		}
	}
	return err
}

func (st *Store) PutUser(ctx context.Context, u *lineupapi.User) error {
	// An empty version asserts "no record exists", which is CreateUser's job.
	// Reaching PutUser with one means the caller never read the record, so the
	// write is unconditioned by construction — refuse it rather than perform a
	// blind overwrite.
	if u.Version == "" {
		return lineupapi.ErrUserConflict
	}
	// The version must be parseable, and it must be compared as a NUMBER.
	// userItem writes `ver` with n(), so a string :v makes `ver = :v` compare an
	// N against an S — which DynamoDB evaluates as false rather than as an
	// error. Every update of an existing record then failed the condition and
	// surfaced as ErrUserConflict forever: ClaimTeam could not bind a team and
	// a token-version bump could not revoke a session, both reported as routine
	// concurrent-write contention. The in-memory double compared only the
	// decimal text, so the conformance suite stayed green throughout.
	cur, err := strconv.ParseInt(string(u.Version), 10, 64)
	if err != nil {
		return fmt.Errorf("ddbuser: unparseable record version %q for %s: %w", u.Version, u.ID, err)
	}
	item, err := st.userItem(u, cur+1)
	if err != nil {
		return err
	}
	_, err = st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(st.table),
		Item:                      item,
		ConditionExpression:       aws.String("ver = :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": n(cur)},
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return lineupapi.ErrUserConflict
		}
		return err
	}
	u.Version = lineupapi.IdentityVersion(strconv.FormatInt(cur+1, 10))
	return nil
}

// ClaimTeam is idempotent for the holder: re-claiming a team you already hold
// succeeds. A reconnect runs this again, and failing there would lock a user
// out of the team they legitimately own.
func (st *Store) ClaimTeam(ctx context.Context, id lineupapi.UserID, teamID string) error {
	_, err := st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(st.table),
		Item:                      map[string]types.AttributeValue{"pk": s(teamPK(teamID)), "sk": s(pointerSK), "uid": s(string(id))},
		ConditionExpression:       aws.String("attribute_not_exists(pk) OR uid = :uid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":uid": s(string(id))},
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			return lineupapi.ErrTeamTaken
		}
		return err
	}

	u, ok, err := st.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return lineupapi.ErrUserConflict
	}
	u.TeamID = teamID
	return st.PutUser(ctx, u)
}

func (st *Store) Credentials(ctx context.Context, id lineupapi.UserID) ([]webauthn.Credential, error) {
	out, err := st.api.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": s(userPK(id)), ":sk": s("CRED#"),
		},
	})
	if err != nil {
		return nil, err
	}
	creds := []webauthn.Credential{}
	for _, item := range out.Items {
		raw, ok := item["cred"].(*types.AttributeValueMemberB)
		if !ok {
			continue
		}
		var c webauthn.Credential
		if err := decodeCred(raw.Value, &c); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func (st *Store) PutCredential(ctx context.Context, id lineupapi.UserID, c webauthn.Credential) error {
	blob, err := encodeCred(c)
	if err != nil {
		return err
	}
	// Unconditional by design: registration writes a sort key that did not
	// exist, and login overwrites one credential only that ceremony could have
	// produced. No other writer touches this key, so there is nothing to lose.
	if _, err := st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table),
		Item: map[string]types.AttributeValue{
			"pk": s(userPK(id)), "sk": s(credSK(c.ID)),
			"cred": &types.AttributeValueMemberB{Value: blob},
		},
	}); err != nil {
		return err
	}
	// Index after the credential: a crash between the two leaves a usable
	// credential that is merely unresolvable by id, which the discoverable path
	// does not need anyway. The reverse order would leave an index entry
	// pointing at nothing.
	_, err = st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table),
		Item:      map[string]types.AttributeValue{"pk": s(credPK(c.ID)), "sk": s(pointerSK), "uid": s(string(id))},
	})
	return err
}

func (st *Store) DeleteCredential(ctx context.Context, id lineupapi.UserID, credID []byte) error {
	// Index first: a revoked credential that still resolves is worse than one
	// merely orphaned, because resolvable means usable.
	if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table), Key: st.key(credPK(credID), pointerSK),
	}); err != nil {
		return err
	}
	if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table), Key: st.key(userPK(id), credSK(credID)),
	}); err != nil {
		return err
	}
	_, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table), Key: st.key(userPK(id), credMetaSK(credID)),
	})
	return err
}

func (st *Store) UserByCredential(ctx context.Context, credID []byte) (lineupapi.UserID, bool, error) {
	out, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table), Key: st.key(credPK(credID), pointerSK),
	})
	if err != nil {
		return "", false, err
	}
	if len(out.Item) == 0 {
		return "", false, nil
	}
	uid, ok := out.Item["uid"].(*types.AttributeValueMemberS)
	if !ok {
		return "", false, fmt.Errorf("ddbuser: credential index item for %s has no uid", b64(credID))
	}
	return lineupapi.UserID(uid.Value), true, nil
}

// credMetaSK is the sort key for one passkey's user-facing meta (name,
// created-at). It sits in the USER# item collection so DeleteUser's pk sweep
// removes it for free, under its OWN key so a rename and the login ceremony's
// sign-counter overwrite cannot contend — and its prefix must never satisfy
// begins_with(sk, "CRED#"), or the credential listing would try to unmarshal
// meta rows as webauthn.Credentials. "CREDMETA#" does not: its fifth byte is
// 'M' where the credential prefix has '#'.
func credMetaSK(credID []byte) string { return "CREDMETA#" + b64(credID) }

const credMetaPrefix = "CREDMETA#"

func (st *Store) PutCredentialMeta(ctx context.Context, id lineupapi.UserID, credID []byte, meta lineupapi.CredentialMeta) error {
	item, err := attributevalue.MarshalMap(meta)
	if err != nil {
		return err
	}
	item["pk"] = s(userPK(id))
	item["sk"] = s(credMetaSK(credID))
	_, err = st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table), Item: item,
	})
	return err
}

func (st *Store) CredentialMetas(ctx context.Context, id lineupapi.UserID) (map[string]lineupapi.CredentialMeta, error) {
	out := map[string]lineupapi.CredentialMeta{}
	var start map[string]types.AttributeValue
	for {
		res, err := st.api.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(st.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": s(userPK(id)), ":sk": s(credMetaPrefix),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range res.Items {
			var m lineupapi.CredentialMeta
			if err := attributevalue.UnmarshalMap(item, &m); err != nil {
				return nil, err
			}
			out[strings.TrimPrefix(strAttr(item["sk"]), credMetaPrefix)] = m
		}
		if len(res.LastEvaluatedKey) == 0 {
			break
		}
		start = res.LastEvaluatedKey
	}
	return out, nil
}

// DeleteUser sweeps the whole USER# item collection (profile, FANTRAX
// connection, every CRED# row), each credential's CRED#→USER index row, and
// then releases the EMAIL#/TEAM# claims — read-check-delete against the uid
// the claim names, so a half-written state can never release somebody else's
// claim. (A conditional delete would close the residual race; at an
// admin-clicks-a-button cadence the read-check-delete window is accepted.)
func (st *Store) DeleteUser(ctx context.Context, id lineupapi.UserID) error {
	u, ok, err := st.GetUser(ctx, id)
	if err != nil {
		return err
	}

	var start map[string]types.AttributeValue
	for {
		out, err := st.api.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(st.table),
			KeyConditionExpression:    aws.String("pk = :pk"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": s(userPK(id))},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return err
		}
		for _, item := range out.Items {
			sk := strAttr(item["sk"])
			// Index row first, like DeleteCredential: a credential that still
			// resolves is usable.
			if strings.HasPrefix(sk, "CRED#") {
				if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
					TableName: aws.String(st.table), Key: st.key(sk, pointerSK),
				}); err != nil {
					return err
				}
			}
			// A push device's PUSHTOKEN# pointer lives outside this item
			// collection for the same reason CRED# rows do (it answers a
			// cross-user question), so the pk sweep cannot reach it.
			if err := st.releasePushTokenFor(ctx, id, sk, item); err != nil {
				return err
			}
			if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(st.table), Key: st.key(userPK(id), sk),
			}); err != nil {
				return err
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}

	if !ok {
		return nil
	}
	for _, pk := range []string{claimPKFor(emailPK, u.Email), claimPKFor(teamPK, u.TeamID)} {
		if pk == "" {
			continue
		}
		got, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(st.table), Key: st.key(pk, pointerSK),
		})
		if err != nil {
			return err
		}
		if got.Item == nil || strAttr(got.Item["uid"]) != string(id) {
			continue
		}
		if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(st.table), Key: st.key(pk, pointerSK),
		}); err != nil {
			return err
		}
	}
	return nil
}

// claimPKFor applies a claim key builder to a possibly-empty value.
func claimPKFor(build func(string) string, val string) string {
	if val == "" {
		return ""
	}
	return build(val)
}

// ListActive scans the table for active profiles. A scan is the deliberate
// choice at this scale — see lineupapi.UserStore.ListActive. Paginated because
// an unpaginated scan silently truncates at 1 MB, and a fan-out that quietly
// drops tenants past that boundary is the failure mode with no symptom.
func (st *Store) ListActive(ctx context.Context) ([]*lineupapi.User, error) {
	return st.scanProfiles(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(st.table),
		FilterExpression: aws.String("sk = :sk AND #st = :active"),
		ExpressionAttributeNames: map[string]string{
			"#st": attrStatus, // STATUS is a DynamoDB reserved word; must be aliased
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":sk": s(profileSK), ":active": s(string(lineupapi.UserActive)),
		},
	})
}

// ListUsers is ListActive without the status filter — the admin directory,
// which must keep showing a parked tenant (see lineupapi.UserStore.ListUsers).
func (st *Store) ListUsers(ctx context.Context) ([]*lineupapi.User, error) {
	return st.scanProfiles(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(st.table),
		FilterExpression: aws.String("sk = :sk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":sk": s(profileSK),
		},
	})
}

func (st *Store) scanProfiles(ctx context.Context, in *dynamodb.ScanInput) ([]*lineupapi.User, error) {
	users := []*lineupapi.User{}
	var start map[string]types.AttributeValue
	for {
		in.ExclusiveStartKey = start
		out, err := st.api.Scan(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, item := range out.Items {
			u, err := unmarshalUser(item)
			if err != nil {
				return nil, err
			}
			users = append(users, u)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	return users, nil
}

// --- EnrollmentStore ---------------------------------------------------------

var _ lineupapi.EnrollmentStore = (*Store)(nil)

func enrollPK(tokenHash string) string { return "ENROLL#" + tokenHash }

const enrollSK = "TOKEN"

// attrUsedAt is the redemption marker. Its ABSENCE means unused, which is what
// RedeemEnrollment's attribute_not_exists(UsedAt) condition asserts.
//
// It has to be deleted explicitly when zero. attributevalue encodes a zero
// time.Time as the STRING "0001-01-01T00:00:00Z" rather than omitting it —
// `json:"omitempty"` does not apply, since attributevalue honours `dynamodbav`
// tags and ignores `json` ones. Left alone, the attribute always exists, the
// condition is never satisfied, and every redemption fails: enrollment links
// would be unusable from the moment they were minted, which for a recovery
// link means no way back into the account at all.
const attrUsedAt = "UsedAt"

// enrollItem marshals an Enrollment, normalising the two time fields so that
// absence carries meaning.
func (st *Store) enrollItem(tokenHash string, e lineupapi.Enrollment) (map[string]types.AttributeValue, error) {
	item, err := attributevalue.MarshalMap(e)
	if err != nil {
		return nil, err
	}
	if e.UsedAt.IsZero() {
		delete(item, attrUsedAt)
	}
	item["pk"] = s(enrollPK(tokenHash))
	item["sk"] = s(enrollSK)
	// expires_at is the table's declared TTL attribute, in epoch seconds. It is
	// HOUSEKEEPING ONLY: TTL deletion is asynchronous and documented to lag by
	// up to 48 hours, so RedeemEnrollment checks expiry itself rather than
	// inferring it from the row being gone.
	if !e.ExpiresAt.IsZero() {
		item["expires_at"] = n(e.ExpiresAt.Unix())
	}
	return item, nil
}

func (st *Store) CreateEnrollment(ctx context.Context, tokenHash string, e lineupapi.Enrollment) error {
	item, err := st.enrollItem(tokenHash, e)
	if err != nil {
		return err
	}

	_, err = st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			// Overwriting would reset a spent link back to unused.
			return lineupapi.ErrUserConflict
		}
		return err
	}
	return nil
}

func (st *Store) GetEnrollment(ctx context.Context, tokenHash string) (lineupapi.Enrollment, bool, error) {
	out, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table), Key: st.key(enrollPK(tokenHash), enrollSK),
	})
	if err != nil {
		return lineupapi.Enrollment{}, false, err
	}
	if len(out.Item) == 0 {
		return lineupapi.Enrollment{}, false, nil
	}
	var e lineupapi.Enrollment
	if err := attributevalue.UnmarshalMap(out.Item, &e); err != nil {
		return lineupapi.Enrollment{}, false, err
	}
	return e, true, nil
}

// RedeemEnrollment burns the link with a conditional write.
//
// The condition — attribute_not_exists(UsedAt) — is what makes this single-use
// under concurrency. Reading, checking Redeemed(), then writing would satisfy
// the interface and let two simultaneous redemptions of one recovery link both
// succeed, because both would read an unused row before either wrote.
//
// Expiry is checked here in code, deliberately not left to the table's TTL:
// TTL deletion lags, so the row can still be present and readable well after
// it expired.
func (st *Store) RedeemEnrollment(ctx context.Context, tokenHash string, now time.Time) (lineupapi.Enrollment, error) {
	e, ok, err := st.GetEnrollment(ctx, tokenHash)
	if err != nil {
		return lineupapi.Enrollment{}, err
	}
	if !ok || e.Redeemed() || e.Expired(now) {
		return lineupapi.Enrollment{}, lineupapi.ErrEnrollmentInvalid
	}

	e.UsedAt = now
	item, err := st.enrollItem(tokenHash, e)
	if err != nil {
		return lineupapi.Enrollment{}, err
	}

	_, err = st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(UsedAt)"),
	})
	if err != nil {
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			// Someone else redeemed it between our read and our write. That is
			// the race this condition exists to lose safely.
			return lineupapi.Enrollment{}, lineupapi.ErrEnrollmentInvalid
		}
		return lineupapi.Enrollment{}, err
	}
	return e, nil
}

// --- ConnectionStore ---------------------------------------------------------

var _ lineupapi.ConnectionStore = (*Store)(nil)

const connSK = "FANTRAX"

// The Fantrax connection is a sibling item of the profile rather than fields on
// it, for the same reason credentials are: it is written by a different actor at
// a different time. The connect task writes this; the API writes the profile.
// Separate keys mean neither can lose the other's change.
func (st *Store) GetConnection(ctx context.Context, uid lineupapi.UserID) (*lineupapi.FantraxConnection, bool, error) {
	out, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table), Key: st.key(userPK(uid), connSK),
	})
	if err != nil {
		return nil, false, err
	}
	if len(out.Item) == 0 {
		return nil, false, nil
	}
	var c lineupapi.FantraxConnection
	if err := attributevalue.UnmarshalMap(out.Item, &c); err != nil {
		return nil, false, err
	}
	return &c, true, nil
}

func (st *Store) PutConnection(ctx context.Context, c *lineupapi.FantraxConnection) error {
	if c.UserID == "" {
		return fmt.Errorf("ddbuser: refusing to write a connection with no user id")
	}
	c.UpdatedAt = time.Now().UTC()
	item, err := attributevalue.MarshalMap(c)
	if err != nil {
		return err
	}
	// Zero times marshal to the string "0001-01-01T00:00:00Z" rather than being
	// omitted (attributevalue honours dynamodbav tags and ignores json ones), so
	// an unverified connection would carry a LastVerifiedAt that reads as a real
	// timestamp from the year 1. Same trap as the enrollment UsedAt.
	if c.LastVerifiedAt.IsZero() {
		delete(item, "LastVerifiedAt")
	}
	item["pk"] = s(userPK(c.UserID))
	item["sk"] = s(connSK)
	_, err = st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table), Item: item,
	})
	return err
}
