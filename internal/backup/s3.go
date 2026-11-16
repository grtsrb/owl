package backup

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"owl/internal/store"
)

// Remote is where backups are copied. The server only ever uploads, lists and reads — it never
// deletes, so the bucket's credentials can (and should) lack delete permission; retention in the
// bucket is a lifecycle rule set by the operator.
type Remote interface {
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	Get(ctx context.Context, name string) (io.ReadCloser, int64, error)
	List(ctx context.Context) ([]Object, error)
	String() string
}

type Object struct {
	Name     string
	Size     int64
	Modified time.Time
}

// S3 works with AWS S3, Hetzner Object Storage, MinIO — anything speaking the S3 API.
type S3 struct {
	c              *minio.Client
	bucket, prefix string
	endpoint       string
}

type S3Config struct {
	Endpoint  string // host[:port], e.g. s3.eu-central-1.amazonaws.com, fsn1.your-objectstorage.com
	Region    string
	Bucket    string
	Prefix    string // e.g. "owl/"
	AccessKey string
	SecretKey string
	Insecure  bool // plain HTTP (local MinIO only)
}

func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("S3 backups need endpoint, bucket, access key and secret key")
	}
	if cfg.Region == "" {
		// An explicit region skips the bucket-location lookup (which some S3-compatible stores lack).
		cfg.Region = "us-east-1"
	}
	c, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: !cfg.Insecure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{c: c, bucket: cfg.Bucket, prefix: prefix, endpoint: cfg.Endpoint}, nil
}

func (s *S3) String() string { return "s3://" + s.bucket + "/" + s.prefix + " (" + s.endpoint + ")" }

func (s *S3) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	_, err := s.c.PutObject(ctx, s.bucket, s.prefix+name, r, size, minio.PutObjectOptions{ContentType: "application/gzip"})
	return err
}

func (s *S3) Get(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	o, err := s.c.GetObject(ctx, s.bucket, s.prefix+name, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	st, err := o.Stat()
	if err != nil {
		o.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, 0, store.ErrNotFound
		}
		return nil, 0, err
	}
	return o, st.Size, nil
}

func (s *S3) List(ctx context.Context) ([]Object, error) {
	var out []Object
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix}) {
		if o.Err != nil {
			return nil, o.Err
		}
		name := path.Base(o.Key)
		if ValidName(name) {
			out = append(out, Object{Name: name, Size: o.Size, Modified: o.LastModified})
		}
	}
	return out, nil
}
