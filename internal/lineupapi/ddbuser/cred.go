package ddbuser

import (
	"encoding/json"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Credentials are stored as a JSON blob in a single binary attribute rather
// than as marshalled DynamoDB attributes.
//
// webauthn.Credential is an upstream type with nested structs and several
// []byte fields, and its shape is not ours to control. Mapping it onto
// attributes would make every upstream field addition a schema question here,
// and would let a rename silently drop a field on read. JSON round-trips
// whatever the library defines, which is the same contract the file store and
// the S3 identity store already use — so a credential written by one backend
// means the same thing to another.
func encodeCred(c webauthn.Credential) ([]byte, error) { return json.Marshal(c) }

func decodeCred(b []byte, c *webauthn.Credential) error { return json.Unmarshal(b, c) }
