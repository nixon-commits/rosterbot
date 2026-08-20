package ddbuser

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/nixon-commits/rosterbot/internal/lineupapi"
)

// Push device key layout, extending the table in the package doc:
//
//	USER#<uid>         PUSHDEVICE#<id>   one item per registered device
//	PUSHTOKEN#<token>  USER              -> uid + did, the cross-user lookup
//
// The PUSHTOKEN# pointer plays the same role CRED# does for passkeys: it
// answers a question ("who holds this token?") that a per-user Query cannot,
// without a GSI. It exists because registration must STEAL a token held by a
// different user (see lineupapi.PushDeviceStore) — the per-user idempotency
// scan finds a match only within the caller's own items.
const pushDevicePrefix = "PUSHDEVICE#"

func pushDeviceSK(id string) string   { return pushDevicePrefix + id }
func pushTokenPK(token string) string { return "PUSHTOKEN#" + token }

var _ lineupapi.PushDeviceStore = (*Store)(nil)

// attrPushToken is the marshalled name of lineupapi.PushDevice.Token —
// attributevalue keys on the Go FIELD NAME, not the json tag (see attrStatus).
const attrPushToken = "Token"

// attrDeviceID is the pointer item's device-id attribute, beside the same
// lowercase "uid" every other pointer item here carries.
const attrDeviceID = "did"

func (st *Store) PutPushDevice(ctx context.Context, uid lineupapi.UserID, d lineupapi.PushDevice) (lineupapi.PushDevice, error) {
	// Idempotency first: a match within the caller's own items keeps its ID
	// and CreatedAt. The set is a handful of items per user, which is why this
	// needs no index of its own.
	existing, err := st.PushDevices(ctx, uid)
	if err != nil {
		return lineupapi.PushDevice{}, err
	}
	for _, e := range existing {
		if e.Token != d.Token {
			continue
		}
		if d.ID == "" {
			d.ID, d.CreatedAt = e.ID, e.CreatedAt
			continue
		}
		// A second row with the same token is a lost race between two
		// concurrent registrations of one device (both reads preceded both
		// writes, both minted an id). Left alone it delivers every
		// notification twice forever — the duplicate is a live token, so
		// ErrDeviceGone never prunes it. Healing here bounds the damage to
		// one launch, since the client re-registers on every launch.
		if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(st.table), Key: st.key(userPK(uid), pushDeviceSK(e.ID)),
		}); err != nil {
			return lineupapi.PushDevice{}, err
		}
	}

	// The steal: the cross-user pointer names the token's current holder, and
	// a DIFFERENT holder's record is deleted as part of this registration —
	// their session on the device is already dead, so no client action of
	// theirs can ever revoke it (see lineupapi.PushDeviceStore). This is a
	// read-check-delete rather than a transaction: the only way two users race
	// on ONE physical device's token is that device re-registering within the
	// same instant under both accounts, and the loser's stale row self-heals
	// on the next launch's re-registration — the same accepted window as
	// DeleteUser's claim release.
	got, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table), Key: st.key(pushTokenPK(d.Token), pointerSK),
	})
	if err != nil {
		return lineupapi.PushDevice{}, err
	}
	if len(got.Item) > 0 {
		if holder := strAttr(got.Item["uid"]); holder != "" && holder != string(uid) {
			if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(st.table),
				Key:       st.key(userPK(lineupapi.UserID(holder)), pushDeviceSK(strAttr(got.Item[attrDeviceID]))),
			}); err != nil {
				return lineupapi.PushDevice{}, err
			}
		}
	}

	if d.ID == "" {
		d.ID = lineupapi.NewPushDeviceID()
	}
	item, err := attributevalue.MarshalMap(d)
	if err != nil {
		return lineupapi.PushDevice{}, err
	}
	item["pk"] = s(userPK(uid))
	item["sk"] = s(pushDeviceSK(d.ID))
	if _, err := st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table), Item: item,
	}); err != nil {
		return lineupapi.PushDevice{}, err
	}

	// Pointer after the device, like PutCredential: a crash between the two
	// leaves a device that delivers but is merely not stealable yet, which the
	// next registration repairs. The reverse order could leave a pointer at
	// nothing.
	if _, err := st.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table),
		Item: map[string]types.AttributeValue{
			"pk": s(pushTokenPK(d.Token)), "sk": s(pointerSK),
			"uid": s(string(uid)), attrDeviceID: s(d.ID),
		},
	}); err != nil {
		return lineupapi.PushDevice{}, err
	}
	return d, nil
}

func (st *Store) PushDevices(ctx context.Context, uid lineupapi.UserID) ([]lineupapi.PushDevice, error) {
	devices := []lineupapi.PushDevice{}
	var start map[string]types.AttributeValue
	for {
		out, err := st.api.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(st.table),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk": s(userPK(uid)), ":sk": s(pushDevicePrefix),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range out.Items {
			var d lineupapi.PushDevice
			if err := attributevalue.UnmarshalMap(item, &d); err != nil {
				return nil, err
			}
			devices = append(devices, d)
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	return devices, nil
}

func (st *Store) DeletePushDevice(ctx context.Context, uid lineupapi.UserID, id string) error {
	// Read first: the pointer release needs the token, which only the device
	// item knows. An absent item is a success — the caller wanted it gone.
	got, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table), Key: st.key(userPK(uid), pushDeviceSK(id)),
	})
	if err != nil {
		return err
	}
	if len(got.Item) == 0 {
		return nil
	}
	token := strAttr(got.Item[attrPushToken])

	// Device item first, pointer second — the opposite of DeleteCredential,
	// because the risk points the other way: the device item is what delivery
	// fans out to, while the pointer only routes the steal. A crash between
	// the two leaves a stale pointer the next registration repairs.
	if _, err := st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table), Key: st.key(userPK(uid), pushDeviceSK(id)),
	}); err != nil {
		return err
	}
	return st.releasePushToken(ctx, token, uid)
}

// releasePushToken deletes the PUSHTOKEN# pointer when it still names this
// user, mirroring the guarded claim release in DeleteUser: a pointer mid-steal
// must never lose another user's entry.
func (st *Store) releasePushToken(ctx context.Context, token string, uid lineupapi.UserID) error {
	if token == "" {
		return nil
	}
	got, err := st.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table), Key: st.key(pushTokenPK(token), pointerSK),
	})
	if err != nil {
		return err
	}
	if len(got.Item) == 0 || strAttr(got.Item["uid"]) != string(uid) {
		return nil
	}
	_, err = st.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table), Key: st.key(pushTokenPK(token), pointerSK),
	})
	return err
}

// releasePushTokensIn is DeleteUser's hook: any swept item that is a push
// device also releases its token pointer, exactly as CRED# rows release their
// lookup rows. Kept beside the code that owns the key shapes.
func (st *Store) releasePushTokenFor(ctx context.Context, uid lineupapi.UserID, sk string, item map[string]types.AttributeValue) error {
	if !strings.HasPrefix(sk, pushDevicePrefix) {
		return nil
	}
	return st.releasePushToken(ctx, strAttr(item[attrPushToken]), uid)
}
