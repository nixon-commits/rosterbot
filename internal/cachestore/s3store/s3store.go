// Package s3store is the S3 adapter for cache.Store. It is isolated here so the
// aws-sdk-go-v2 dependency stays out of the zero-dep internal/cache leaf.
package s3store

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	cache "github.com/nixon-commits/rosterbot/internal/cache"
)

// api is the slice of the S3 client this adapter needs (fakeable in tests).
type api interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// Store implements cache.Store against an S3 bucket+prefix, one object per key.
type Store struct {
	client api
	bucket string
	prefix string
}

// New builds a Store using the default AWS credential/region chain. prefix
// should end in "/", e.g. "cache/".
func New(ctx context.Context, bucket, prefix string) (*Store, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Store{client: s3.NewFromConfig(cfg), bucket: bucket, prefix: prefix}, nil
}

func (s *Store) objKey(key string) string { return s.prefix + key + ".json" }

func (s *Store) Get(key string) ([]byte, bool, error) {
	out, err := s.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: &s.bucket, Key: ptr(s.objKey(key)),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		var nf *types.NotFound
		if errors.As(err, &nsk) || errors.As(err, &nf) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer out.Body.Close()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (s *Store) Put(key string, data []byte) error {
	_, err := s.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: &s.bucket, Key: ptr(s.objKey(key)), Body: bytes.NewReader(data),
	})
	return err
}

func (s *Store) Remove(key string) error {
	_, err := s.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: &s.bucket, Key: ptr(s.objKey(key)),
	})
	return err
}

func ptr(s string) *string { return &s }

// Compile-time assertion: *Store must satisfy cache.Store.
var _ cache.Store = (*Store)(nil)
