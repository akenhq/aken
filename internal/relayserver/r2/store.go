// SPDX-License-Identifier: Apache-2.0
package r2

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/akenhq/aken/protocol"
	"github.com/akenhq/aken/relay"
	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Config struct{ AccountID, Bucket, AccessKeyID, SecretAccessKey string }

func ConfigFromEnv() (Config, error) {
	var cfg Config
	var missing []string
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"AKEN_R2_ACCOUNT_ID", &cfg.AccountID}, {"AKEN_R2_BUCKET", &cfg.Bucket},
		{"AKEN_R2_ACCESS_KEY_ID", &cfg.AccessKeyID}, {"AKEN_R2_SECRET_ACCESS_KEY", &cfg.SecretAccessKey},
	} {
		*field.value = os.Getenv(field.name)
		if *field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) != 0 {
		return Config{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}

type Store struct {
	client *s3.Client
	bucket string
}

var _ relay.Store = (*Store)(nil)

func New(_ context.Context, cfg Config) (*Store, error) {
	client := s3.NewFromConfig(aws.Config{
		Region:                     "auto",
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("https://" + cfg.AccountID + ".r2.cloudflarestorage.com")
	})
	return &Store{client: client, bucket: cfg.Bucket}, nil
}

type metadata struct {
	Mode           string    `json:"mode,omitempty"`
	CollectorKey   string    `json:"collector_key,omitempty"`
	CollectorMAC   string    `json:"collector_mac,omitempty"`
	CredentialHash string    `json:"credential_hash"`
	ExpiresAt      time.Time `json:"expires_at"`
	ChunkCount     uint32    `json:"chunk_count"`
	CreatedAt      time.Time `json:"created_at"`
}

func prefix(id protocol.SessionID) string { return "sessions/" + id.String() + "/" }
func chunkKey(id protocol.SessionID, index uint64) string {
	return fmt.Sprintf("%schunks/%05d", prefix(id), index)
}

func storeError(op string, err error) error {
	if err == nil {
		return nil
	}
	var missing *types.NoSuchKey
	var response *awshttp.ResponseError
	switch {
	case errors.As(err, &missing):
		return relay.ErrNotFound
	case errors.As(err, &response):
		switch response.HTTPStatusCode() {
		case 404:
			return relay.ErrNotFound
		case 412:
			return relay.ErrExists
		}
	}
	return fmt.Errorf("r2 %s: %w", op, err)
}

func (s *Store) put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket, Key: &key, Body: bytes.NewReader(body), IfNoneMatch: aws.String("*"),
	})
	return storeError("put object", err)
}

func (s *Store) get(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, storeError("get object", err)
	}
	defer func() { _ = obj.Body.Close() }()
	body, err := io.ReadAll(obj.Body)
	return body, storeError("read object", err)
}

func (s *Store) head(ctx context.Context, key string) error {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	return storeError("head object", err)
}

func (s *Store) CreateSession(ctx context.Context, id protocol.SessionID, meta relay.SessionMeta) error {
	body, err := json.Marshal(metadata{meta.Mode, protocol.EncodeKey(meta.CollectorKey), protocol.EncodeKey(meta.CollectorMAC), hex.EncodeToString(meta.CredentialHash[:]), meta.ExpiresAt.UTC(), meta.ChunkCount, time.Now().UTC()})
	if err != nil {
		return storeError("encode metadata", err)
	}
	return s.put(ctx, prefix(id)+"meta.json", body)
}

func (s *Store) readMeta(ctx context.Context, id protocol.SessionID) (relay.SessionMeta, error) {
	body, err := s.get(ctx, prefix(id)+"meta.json")
	if err != nil {
		return relay.SessionMeta{}, err
	}
	var meta metadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return relay.SessionMeta{}, storeError("decode metadata", err)
	}
	hash, err := hex.DecodeString(meta.CredentialHash)
	if err != nil || len(hash) != 32 || meta.CredentialHash != strings.ToLower(meta.CredentialHash) {
		return relay.SessionMeta{}, errors.New("r2 decode metadata: invalid credential hash")
	}
	key, keyOK := protocol.DecodeKey(meta.CollectorKey)
	mac, macOK := protocol.DecodeKey(meta.CollectorMAC)
	if (meta.Mode == "session" || meta.CollectorKey != "") && !keyOK {
		return relay.SessionMeta{}, storeError("decode metadata", errors.New("invalid collector key"))
	}
	if (meta.Mode == "session" || meta.CollectorMAC != "") && !macOK {
		return relay.SessionMeta{}, storeError("decode metadata", errors.New("invalid collector MAC"))
	}
	return relay.SessionMeta{Mode: meta.Mode, CollectorKey: key, CollectorMAC: mac, CredentialHash: [32]byte(hash), ExpiresAt: meta.ExpiresAt, ChunkCount: meta.ChunkCount}, nil
}

func (s *Store) objects(ctx context.Context, keyPrefix string) ([]types.Object, error) {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &keyPrefix})
	var objects []types.Object
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, storeError("list objects", err)
		}
		objects = append(objects, page.Contents...)
	}
	return objects, nil
}

func (s *Store) Session(ctx context.Context, id protocol.SessionID) (relay.SessionMeta, error) {
	meta, err := s.readMeta(ctx, id)
	if err != nil {
		return meta, err
	}
	objects, err := s.objects(ctx, prefix(id)+"chunks/")
	if err != nil {
		return meta, err
	}
	for range objects {
		meta.ChunksStored++
	}
	err = s.head(ctx, prefix(id)+"manifest")
	meta.ManifestStored = err == nil
	if errors.Is(err, relay.ErrNotFound) {
		err = nil
	}
	return meta, err
}

func (s *Store) PutChunk(ctx context.Context, id protocol.SessionID, index uint64, ciphertext []byte) error {
	if err := s.head(ctx, prefix(id)+"meta.json"); err != nil {
		return err
	}
	return s.put(ctx, chunkKey(id, index), ciphertext)
}

func (s *Store) GetChunk(ctx context.Context, id protocol.SessionID, index uint64) ([]byte, error) {
	return s.get(ctx, chunkKey(id, index))
}

func (s *Store) PutManifest(ctx context.Context, id protocol.SessionID, ciphertext []byte) error {
	if err := s.head(ctx, prefix(id)+"meta.json"); err != nil {
		return err
	}
	return s.put(ctx, prefix(id)+"manifest", ciphertext)
}

func (s *Store) GetManifest(ctx context.Context, id protocol.SessionID) ([]byte, error) {
	return s.get(ctx, prefix(id)+"manifest")
}

func (s *Store) DeleteSession(ctx context.Context, id protocol.SessionID) error {
	objects, err := s.objects(ctx, prefix(id))
	if err != nil {
		return err
	}
	if len(objects) == 0 {
		return relay.ErrNotFound
	}
	for start := 0; start < len(objects); start += 1000 {
		var keys []types.ObjectIdentifier
		for _, obj := range objects[start:min(start+1000, len(objects))] {
			keys = append(keys, types.ObjectIdentifier{Key: obj.Key})
		}
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: &s.bucket, Delete: &types.Delete{Objects: keys, Quiet: aws.Bool(true)}})
		if err != nil {
			return storeError("delete objects", err)
		}
		if len(out.Errors) != 0 {
			return errors.New("r2 delete objects: object deletion failed")
		}
	}
	return nil
}

func (s *Store) Sweep(ctx context.Context, now time.Time) (deleted, live int, err error) {
	return s.scan(ctx, now, true)
}

func (s *Store) LiveSessions(ctx context.Context) (int, error) {
	_, live, err := s.scan(ctx, time.Now().UTC(), false)
	return live, err
}

func (s *Store) scan(ctx context.Context, now time.Time, sweep bool) (deleted, live int, err error) {
	pages := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String("sessions/"), Delimiter: aws.String("/")})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return deleted, live, storeError("list sessions", err)
		}
		for _, p := range page.CommonPrefixes {
			id, ok := protocol.ParseSessionID(strings.TrimSuffix(strings.TrimPrefix(aws.ToString(p.Prefix), "sessions/"), "/"))
			if !ok {
				continue
			}
			meta, err := s.readMeta(ctx, id)
			remove := false
			switch {
			case errors.Is(err, relay.ErrNotFound):
				if !sweep {
					continue
				}
				objects, err := s.objects(ctx, prefix(id))
				if err != nil {
					return deleted, live, err
				}
				var newest time.Time
				for _, obj := range objects {
					if obj.LastModified != nil && obj.LastModified.After(newest) {
						newest = *obj.LastModified
					}
				}
				remove = !newest.IsZero() && newest.Before(now.Add(-time.Hour))
			case err != nil:
				return deleted, live, err
			case meta.ExpiresAt.After(now):
				live++
			default:
				remove = true
			}
			if sweep && remove {
				if err := s.DeleteSession(ctx, id); err != nil {
					if errors.Is(err, relay.ErrNotFound) {
						continue
					}
					return deleted, live, err
				}
				deleted++
			}
		}
	}
	return deleted, live, nil
}
