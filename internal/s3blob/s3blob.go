// Package s3blob is the one place aws-sdk-go-v2's S3 client is constructed and
// driven. Every durable-state adapter in this repo — internal/cachestore/s3store,
// internal/ndjsonstore/s3ndjson and the seven stores in
// internal/lineupapi/s3lineup — is a thin shim over a Blob, so credential
// resolution, prefix joining, not-found mapping, body reads and list pagination
// exist once rather than eight times.
//
// It deliberately does NOT unify the Store interfaces above it, and must not be
// used to. cache.Store is a delete-capable keyed TTL cache with no enumeration;
// ndjsonstore.Store is an enumerable append-only partitioned log with no delete;
// the lineup API's stores are per-artifact reader/writer pairs. Those shapes
// encode real semantic differences between the Cache and the durable stores (see
// docs/adr/0001, docs/adr/0002). Only the SDK plumbing beneath them is shared.
//
// The AWS SDK stays out of the leaves it serves: internal/cache and
// internal/ndjsonstore declare their Store seams with no dependency here, and
// only the adapter packages (imported by cmd, lambda and statestore) import
// this one.
package s3blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// API is the slice of the S3 client a Blob drives. It is an interface so tests
// can inject a fake; it is not a security boundary — what a given deployment may
// actually do is bounded by its IAM policy (the lineup Lambda, for instance, is
// granted s3:ListBucket without GetObject on the state bucket).
type API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Object is one listed object's metadata. Key is relative to the Blob's prefix.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Blob is a bucket+prefix scoped view of S3. Every key handed to or returned
// from a Blob is relative to prefix: the prefix is joined on the way in and
// stripped on the way out, so key-derived fields parse identically against S3
// and against the local-directory backend each adapter also has (the Analysis
// Store's system= segment, the run ledger's inverted-timestamp ordering).
type Blob struct {
	client API
	bucket string
	prefix string
}

// New builds a Blob using the default AWS credential/region chain. prefix should
// end in "/" (e.g. "cache/"), or be empty to scope the whole bucket.
func New(ctx context.Context, bucket, prefix string) (*Blob, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return NewWithClient(s3.NewFromConfig(cfg), bucket, prefix), nil
}

// NewWithClient builds a Blob over an explicit client. Tests use it to inject a
// fake; production goes through New.
func NewWithClient(client API, bucket, prefix string) *Blob {
	return &Blob{client: client, bucket: bucket, prefix: prefix}
}

// Bucket reports the bucket this Blob is scoped to.
func (b *Blob) Bucket() string { return b.bucket }

// Key joins the Blob's prefix onto a relative key, yielding the full object key.
func (b *Blob) Key(key string) string { return b.prefix + key }

// Get reads the object at key. A missing object is (nil, false, nil) rather than
// an error: absence is a normal outcome for every caller here — a cache miss, a
// run with no captured output yet, an unregistered passkey.
func (b *Blob) Get(ctx context.Context, key string) ([]byte, bool, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.bucket, Key: ptr(b.Key(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Put writes data at key with no explicit content type.
func (b *Blob) Put(ctx context.Context, key string, data []byte) error {
	return b.put(ctx, key, data, nil)
}

// PutJSON writes data at key tagged application/json.
func (b *Blob) PutJSON(ctx context.Context, key string, data []byte) error {
	return b.put(ctx, key, data, ptr("application/json"))
}

func (b *Blob) put(ctx context.Context, key string, data []byte, contentType *string) error {
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &b.bucket,
		Key:         ptr(b.Key(key)),
		Body:        bytes.NewReader(data),
		ContentType: contentType,
	})
	return err
}

// Delete removes the object at key. Deleting a key that isn't there is not an
// error (S3's own semantics).
func (b *Blob) Delete(ctx context.Context, key string) error {
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &b.bucket, Key: ptr(b.Key(key)),
	})
	return err
}

// Walk pages through every object under prefix (itself relative to the Blob's
// prefix), calling fn once per object in S3's lexicographic key order. Keys
// reported to fn are relative to the Blob's prefix, not to the walk prefix — so
// a Blob scoped at the bucket root reports whole keys and one scoped at
// analysis/ reports grades/dt=…/… for the same objects.
//
// fn returns false to stop early; Walk then returns nil. Following
// NextContinuationToken is what makes stopping early safe: a caller that wants
// the newest N objects can stop at N without a single-page listing silently
// hiding the rest, which is the bug class s3lineup's run-ledger listing hit
// (rosterbot-432).
func (b *Blob) Walk(ctx context.Context, prefix string, fn func(Object) bool) error {
	full := b.Key(prefix)
	var token *string
	for {
		out, err := b.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &b.bucket, Prefix: &full, ContinuationToken: token,
		})
		if err != nil {
			return err
		}
		for _, o := range out.Contents {
			if o.Key == nil {
				continue
			}
			obj := Object{Key: strings.TrimPrefix(*o.Key, b.prefix)}
			if o.Size != nil {
				obj.Size = *o.Size
			}
			if o.LastModified != nil {
				obj.LastModified = *o.LastModified
			}
			if !fn(obj) {
				return nil
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated || out.NextContinuationToken == nil {
			return nil
		}
		token = out.NextContinuationToken
	}
}

// List returns every key under prefix, relative to the Blob's prefix. A prefix
// with nothing under it yields no keys and no error — an empty store is not an
// error.
func (b *Blob) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	err := b.Walk(ctx, prefix, func(o Object) bool {
		keys = append(keys, o.Key)
		return true
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// isNotFound recognizes both shapes S3 returns for a missing object: NoSuchKey
// from GetObject, and NotFound from the HeadObject-style 404 the SDK surfaces
// when the caller lacks s3:ListBucket.
func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	var nf *types.NotFound
	return errors.As(err, &nsk) || errors.As(err, &nf)
}

func ptr(s string) *string { return &s }
