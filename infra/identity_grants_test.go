package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/jsii-runtime-go"
)

// synthTemplate builds the stack and returns its CloudFormation template as a
// decoded map. Vpc_FromLookup needs a concrete account/region or it cannot run
// at synth time, so the env is pinned to the real deployment target; with no
// cached context CDK substitutes a dummy VPC, which is fine — nothing asserted
// here depends on the VPC.
func synthTemplate(t *testing.T) map[string]any {
	t.Helper()
	app := awscdk.NewApp(nil)
	stack := NewInfraStack(app, "TestStack", &InfraStackProps{
		StackProps: awscdk.StackProps{
			Env: &awscdk.Environment{
				Account: jsii.String("476646938644"),
				Region:  jsii.String("us-west-1"),
			},
		},
	})
	raw, err := json.Marshal(assertions.Template_FromStack(stack, nil).ToJSON())
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal template: %v", err)
	}
	return out
}

// kmsActionsByRole returns, for every IAM policy in the template, the kms:*
// actions it grants keyed by a crude label for the role it is attached to.
// The label is derived from the role's logical id appearing anywhere in the
// policy's Roles field, which is enough to tell apiFn from the task role.
func kmsActionsByRole(t *testing.T, tmpl map[string]any) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	resources, _ := tmpl["Resources"].(map[string]any)
	for _, v := range resources {
		res, _ := v.(map[string]any)
		if res["Type"] != "AWS::IAM::Policy" {
			continue
		}
		props, _ := res["Properties"].(map[string]any)
		rolesJSON, _ := json.Marshal(props["Roles"])
		label := "other"
		switch {
		case strings.Contains(string(rolesJSON), "LineupApi"):
			label = "apiFn"
		case strings.Contains(string(rolesJSON), "TaskTaskRole"):
			label = "taskRole"
		}
		doc, _ := props["PolicyDocument"].(map[string]any)
		stmts, _ := doc["Statement"].([]any)
		for _, s := range stmts {
			stmt, _ := s.(map[string]any)
			var actions []string
			switch a := stmt["Action"].(type) {
			case string:
				actions = []string{a}
			case []any:
				for _, x := range a {
					if str, ok := x.(string); ok {
						actions = append(actions, str)
					}
				}
			}
			for _, a := range actions {
				if strings.HasPrefix(a, "kms:") {
					out[label] = append(out[label], a)
				}
			}
		}
	}
	return out
}

// TestFantraxCredKey_ApiFunctionCannotDecrypt is the guard for the one property
// that makes storing tenants' Fantrax passwords defensible at all: the Lambda
// behind the public Function URL (AuthType: NONE) can encrypt a credential but
// can never read one back. A compromise of that internet-facing surface then
// yields ciphertext and no key.
//
// The test exists because the obvious way to write the grant is wrong.
// awskms.Key.GrantEncrypt() also grants kms:ReEncrypt*, and ReEncryptFrom is a
// decryption primitive in disguise: a principal holding it can re-encrypt the
// ciphertext under a key it controls (supplying the matching ReEncryptTo from
// that key's own policy) and decrypt it there. KMS never returns plaintext
// during the call, so it reads as safe while leaving the attacker in exactly
// the position kms:Decrypt would have. Anyone "tidying" this back into
// GrantEncrypt fails here.
func TestFantraxCredKey_ApiFunctionCannotDecrypt(t *testing.T) {
	byRole := kmsActionsByRole(t, synthTemplate(t))

	got := byRole["apiFn"]
	if len(got) == 0 {
		t.Fatal("apiFn has no kms actions at all; it must be able to encrypt a credential")
	}
	for _, a := range got {
		if a != "kms:Encrypt" {
			t.Errorf("apiFn granted %q; it must hold kms:Encrypt and nothing else. "+
				"kms:Decrypt reads credentials directly, and kms:ReEncrypt* (which "+
				"GrantEncrypt adds) reaches the same end state via a key the caller "+
				"controls. Write the statement out by hand.", a)
		}
	}
}

// TestFantraxCredKey_TaskRoleCanDecrypt is the other half: the split is only
// meaningful if the principal that actually needs a credential still has it.
// A test asserting apiFn cannot decrypt passes trivially if nothing can.
func TestFantraxCredKey_TaskRoleCanDecrypt(t *testing.T) {
	byRole := kmsActionsByRole(t, synthTemplate(t))

	var canDecrypt bool
	for _, a := range byRole["taskRole"] {
		if a == "kms:Decrypt" {
			canDecrypt = true
		}
	}
	if !canDecrypt {
		t.Errorf("task role kms actions = %v, want kms:Decrypt — the connect task is "+
			"the only thing that may read a Fantrax credential, and without this "+
			"the asymmetry test above passes for the wrong reason", byRole["taskRole"])
	}
}

// TestIdentityTable_SurvivesStackReplacement pins the properties that make the
// identity table safe to depend on. Unlike every other store in this tree, its
// contents cannot be regenerated from an upstream: losing it locks every user
// out of the dashboard and orphans their Fantrax connection.
func TestIdentityTable_SurvivesStackReplacement(t *testing.T) {
	tmpl := synthTemplate(t)
	resources, _ := tmpl["Resources"].(map[string]any)

	var found bool
	for _, v := range resources {
		res, _ := v.(map[string]any)
		typ, _ := res["Type"].(string)
		if !strings.HasPrefix(typ, "AWS::DynamoDB::") {
			continue
		}
		found = true
		if got := res["DeletionPolicy"]; got != "Retain" {
			t.Errorf("identity table DeletionPolicy = %v, want Retain", got)
		}
		props, _ := res["Properties"].(map[string]any)
		if !pitrEnabled(props) {
			t.Error("identity table does not have point-in-time recovery ENABLED; its " +
				"contents are the one thing here that cannot be refetched from an upstream")
		}
		// ENROLL# items are single-use enrollment tokens carrying an absolute
		// expiry. Without TTL an unredeemed invite stays a live credential
		// forever, which is the opposite of "single-use, short-lived".
		if !ttlOnExpiresAt(props) {
			t.Error("identity table has no ENABLED TTL on expires_at; unredeemed " +
				"enrollment tokens would never expire")
		}
	}
	if !found {
		t.Fatal("no DynamoDB table in the synthesized stack")
	}
}

// pitrEnabled reports whether every replica has point-in-time recovery on.
//
// It navigates to the VALUE rather than searching the marshalled JSON for a
// key name. The previous check was strings.Contains(props,
// "PointInTimeRecovery"), and the synthesized key is
// "PointInTimeRecoveryEnabled" — so the needle was a proper PREFIX of a key
// that is present whether the flag is true or false. The guard protecting the
// one store here whose contents cannot be refetched could not fail.
//
// Returns false for zero replicas: "nothing to check" must not read as "all
// good" on a table this one guards.
func pitrEnabled(props map[string]any) bool {
	replicas, ok := props["Replicas"].([]any)
	if !ok || len(replicas) == 0 {
		return false
	}
	for _, r := range replicas {
		rep, ok := r.(map[string]any)
		if !ok {
			return false
		}
		spec, ok := rep["PointInTimeRecoverySpecification"].(map[string]any)
		if !ok {
			return false
		}
		if enabled, _ := spec["PointInTimeRecoveryEnabled"].(bool); !enabled {
			return false
		}
	}
	return true
}

// ttlOnExpiresAt reports whether TTL is ENABLED and points at expires_at.
//
// Same failure as pitrEnabled: the old check searched for the string
// "expires_at", which is present in the specification whether Enabled is true
// or false. An unredeemed enrollment token would then stay a live credential
// forever, which is the opposite of "single-use, short-lived".
func ttlOnExpiresAt(props map[string]any) bool {
	spec, ok := props["TimeToLiveSpecification"].(map[string]any)
	if !ok {
		return false
	}
	if enabled, _ := spec["Enabled"].(bool); !enabled {
		return false
	}
	name, _ := spec["AttributeName"].(string)
	return name == "expires_at"
}

// TestDurabilityGuardsDetectTheDisabledCase is a test OF THE GUARD, because the
// guard it replaces could not fail.
//
// The old assertions were strings.Contains(props, "PointInTimeRecovery") and
// strings.Contains(props, "expires_at"). Both needles are present in the
// synthesized template whether the feature is enabled or disabled — the first
// is a proper prefix of "PointInTimeRecoveryEnabled" — so flipping either flag
// to false left the guard green. On the one store in this tree whose contents
// cannot be refetched from an upstream.
//
// A guard that cannot go red is worth nothing, and reads as protection. These
// cases are the disabled templates it must now reject.
func TestDurabilityGuardsDetectTheDisabledCase(t *testing.T) {
	on := map[string]any{
		"Replicas": []any{map[string]any{
			"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true},
		}},
		"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": true},
	}
	if !pitrEnabled(on) || !ttlOnExpiresAt(on) {
		t.Fatal("the guards reject a correctly configured table")
	}

	for _, tc := range []struct {
		name  string
		props map[string]any
		pitr  bool
		ttl   bool
	}{
		{
			name: "PITR disabled — the case the old substring check could not see",
			props: map[string]any{
				"Replicas": []any{map[string]any{
					"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": false},
				}},
				"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": true},
			},
			pitr: false, ttl: true,
		},
		{
			name: "TTL disabled — an unredeemed invite would never expire",
			props: map[string]any{
				"Replicas": []any{map[string]any{
					"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true},
				}},
				"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": false},
			},
			pitr: true, ttl: false,
		},
		{
			name:  "no replicas at all — nothing to check must not read as all good",
			props: map[string]any{"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": true}},
			pitr:  false, ttl: true,
		},
		{
			name: "TTL on the wrong attribute",
			props: map[string]any{
				"Replicas": []any{map[string]any{
					"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true},
				}},
				"TimeToLiveSpecification": map[string]any{"AttributeName": "created_at", "Enabled": true},
			},
			pitr: true, ttl: false,
		},
		{
			name: "one replica of two disabled",
			props: map[string]any{
				"Replicas": []any{
					map[string]any{"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": true}},
					map[string]any{"PointInTimeRecoverySpecification": map[string]any{"PointInTimeRecoveryEnabled": false}},
				},
				"TimeToLiveSpecification": map[string]any{"AttributeName": "expires_at", "Enabled": true},
			},
			pitr: false, ttl: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pitrEnabled(tc.props); got != tc.pitr {
				t.Errorf("pitrEnabled = %v, want %v", got, tc.pitr)
			}
			if got := ttlOnExpiresAt(tc.props); got != tc.ttl {
				t.Errorf("ttlOnExpiresAt = %v, want %v", got, tc.ttl)
			}
		})
	}
}
