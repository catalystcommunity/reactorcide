package objects

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// S3ObjectStore implements ObjectStore using AWS S3 or S3-compatible storage
type S3ObjectStore struct {
	client *s3.Client
	bucket string
	prefix string
}

// S3Config contains configuration for the S3 object store
type S3Config struct {
	Bucket    string
	Prefix    string
	Region    string
	Endpoint  string // Optional: for S3-compatible services like MinIO, SeaweedFS
	AccessKey string
	SecretKey string
}

// NewS3ObjectStore creates a new S3-based object store
func NewS3ObjectStore(cfg S3Config) (*S3ObjectStore, error) {
	// Build AWS config options
	var opts []func(*config.LoadOptions) error

	// Set region
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	opts = append(opts, config.WithRegion(region))

	// Set credentials if provided
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	// Load AWS config
	awsCfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client with custom endpoint if specified
	var clientOpts []func(*s3.Options)
	if cfg.Endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // Required for most S3-compatible services
		})
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)

	return &S3ObjectStore{
		client: client,
		bucket: cfg.Bucket,
		prefix: cfg.Prefix,
	}, nil
}

// fullKey returns the full object key with prefix
func (s *S3ObjectStore) fullKey(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + key
}

// Put stores an object in S3
func (s *S3ObjectStore) Put(ctx context.Context, key string, data io.Reader, contentType string) error {
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
		Body:   data,
	}

	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to put object: %w", err)
	}

	return nil
}

// Get retrieves an object from S3
func (s *S3ObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get object: %w", err)
	}

	return output.Body, nil
}

// isS3NotFound checks if an S3 error indicates the object was not found
func isS3NotFound(err error) bool {
	// Check for typed NoSuchKey error
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}

	// Check for HTTP 404 response (works with S3-compatible services like SeaweedFS)
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		return true
	}

	return false
}

// GetURL returns a pre-signed URL for the object
func (s *S3ObjectStore) GetURL(ctx context.Context, key string, expires time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s.client)

	request, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = expires
	})
	if err != nil {
		return "", fmt.Errorf("failed to create presigned URL: %w", err)
	}

	return request.URL, nil
}

// Delete removes an object from S3
func (s *S3ObjectStore) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// Exists checks if an object exists in S3
func (s *S3ObjectStore) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}

	return true, nil
}

// List returns objects with the given prefix
func (s *S3ObjectStore) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	fullPrefix := s.fullKey(prefix)

	var objects []ObjectInfo
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(fullPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			objects = append(objects, ObjectInfo{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
			})
		}
	}

	return objects, nil
}

// NewS3ObjectStoreFromEnv creates an S3 object store using environment variables
func NewS3ObjectStoreFromEnv(bucket, prefix string) (*S3ObjectStore, error) {
	cfg := S3Config{
		Bucket:    bucket,
		Prefix:    prefix,
		Region:    os.Getenv("AWS_REGION"),
		Endpoint:  os.Getenv("REACTORCIDE_S3_ENDPOINT"),
		AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}

	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	return NewS3ObjectStore(cfg)
}
