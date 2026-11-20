package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/joshuadouch/berth/internal/model"
	"github.com/joshuadouch/berth/internal/store"
)

// Off-site copies of backups in S3-compatible storage. Objects are stored as
// <prefix>/<site>/<file>, the same names as on disk.

type remoteCache struct {
	mu      sync.Mutex
	at      time.Time
	key     string
	backups []model.Backup
	err     error
}

func (e *Engine) s3(ctx context.Context) (*minio.Client, model.S3Settings, error) {
	st, err := e.Store.Settings(ctx)
	if err != nil {
		return nil, model.S3Settings{}, err
	}
	c := st.S3
	if !c.Enabled {
		return nil, c, errS3Off
	}
	return e.s3Client(c)
}

var errS3Off = errors.New("off-site backups are not set up")

func (e *Engine) s3Client(c model.S3Settings) (*minio.Client, model.S3Settings, error) {
	endpoint, plain := strings.CutPrefix(c.Endpoint, "http://")
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
		Secure: !e.S3Insecure && !plain,
		Region: c.Region,
	}
	if c.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	if e.S3Transport != nil {
		opts.Transport = e.S3Transport
	}
	cl, err := minio.New(endpoint, opts)
	return cl, c, err
}

func objectKey(c model.S3Settings, site, file string) string {
	return path.Join(c.Prefix, site, file)
}

// TestS3 checks that the bucket can be written, read and deleted.
func (e *Engine) TestS3(ctx context.Context, c model.S3Settings) error {
	cl, _, err := e.s3Client(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := path.Join(c.Prefix, ".berth-test")
	body := strings.NewReader("berth " + time.Now().UTC().Format(time.RFC3339))
	if _, err := cl.PutObject(ctx, c.Bucket, key, body, body.Size(), minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return fmt.Errorf("upload test: %w", explainS3(err))
	}
	if err := cl.RemoveObject(ctx, c.Bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete test: %w", explainS3(err))
	}
	return nil
}

func explainS3(err error) error {
	var r minio.ErrorResponse
	if errors.As(err, &r) && r.Code != "" {
		switch r.Code {
		case "NoSuchBucket":
			return errors.New("the bucket does not exist")
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return fmt.Errorf("%s: check the key ID, secret and that the key may use this bucket", r.Code)
		}
		return fmt.Errorf("%s: %s", r.Code, r.Message)
	}
	return err
}

// uploadRemote copies a local backup off-site. Failures never fail the backup.
func (e *Engine) uploadRemote(ctx context.Context, site, file, local string, logf Logf) {
	cl, c, err := e.s3(ctx)
	if errors.Is(err, errS3Off) {
		return
	}
	if err == nil {
		logf("Uploading to %s/%s", c.Bucket, objectKey(c, site, file))
		_, err = cl.FPutObject(ctx, c.Bucket, objectKey(c, site, file), local, minio.PutObjectOptions{ContentType: "application/gzip"})
	}
	e.forgetRemote()
	if err != nil {
		err = explainS3(err)
		logf("Off-site upload failed: %v", err)
		e.Store.AddEvent(ctx, "error", "backup", site, "Off-site upload of "+file+" failed: "+err.Error())
		return
	}
	logf("Uploaded off-site")
}

func (e *Engine) forgetRemote() {
	e.remote.mu.Lock()
	e.remote.at = time.Time{}
	e.remote.mu.Unlock()
}

// remoteBackups lists every off-site backup, cached for a minute.
func (e *Engine) remoteBackups(ctx context.Context) ([]model.Backup, error) {
	cl, c, err := e.s3(ctx)
	if errors.Is(err, errS3Off) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key := c.Endpoint + "|" + c.Bucket + "|" + c.Prefix
	e.remote.mu.Lock()
	defer e.remote.mu.Unlock()
	if e.remote.key == key && time.Since(e.remote.at) < time.Minute {
		return e.remote.backups, e.remote.err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prefix := ""
	if c.Prefix != "" {
		prefix = c.Prefix + "/"
	}
	var out []model.Backup
	var lerr error
	for obj := range cl.ListObjects(ctx, c.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			lerr = explainS3(obj.Err)
			break
		}
		rest := strings.TrimPrefix(obj.Key, prefix)
		site, file, ok := strings.Cut(rest, "/")
		if !ok || !model.ValidName(site) || !ValidBackupName(file) || !strings.HasPrefix(file, site+"-") {
			continue
		}
		out = append(out, model.Backup{Site: site, File: file, Size: obj.Size, Time: backupTime(file, obj.LastModified), Remote: true})
	}
	e.remote.key, e.remote.at, e.remote.backups, e.remote.err = key, time.Now(), out, lerr
	return out, lerr
}

// RemoteError is the last error listing off-site backups, for the UI.
func (e *Engine) RemoteError(ctx context.Context) string {
	if _, err := e.remoteBackups(ctx); err != nil {
		return err.Error()
	}
	return ""
}

// fetchRemote downloads an off-site backup into the backup folder.
func (e *Engine) fetchRemote(ctx context.Context, site, file string, logf Logf) (string, error) {
	cl, c, err := e.s3(ctx)
	if err != nil {
		return "", err
	}
	dir, err := e.backupDir(ctx, site)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, file)
	tmp := dst + ".partial"
	defer os.Remove(tmp)
	logf("Downloading %s from %s", file, c.Bucket)
	if err := cl.FGetObject(ctx, c.Bucket, objectKey(c, site, file), tmp, minio.GetObjectOptions{}); err != nil {
		var r minio.ErrorResponse
		if errors.As(err, &r) && r.Code == "NoSuchKey" {
			return "", store.ErrNotFound
		}
		return "", explainS3(err)
	}
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// LocalBackup returns a backup's path on disk, downloading it first when it
// is only off-site.
func (e *Engine) LocalBackup(ctx context.Context, site, file string, logf Logf) (string, error) {
	p, err := e.BackupPath(ctx, site, file)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, _, s3err := e.s3(ctx); s3err != nil {
		return "", err
	}
	return e.fetchRemote(ctx, site, file, logf)
}

func (e *Engine) deleteRemote(ctx context.Context, site, file string) error {
	cl, c, err := e.s3(ctx)
	if errors.Is(err, errS3Off) {
		return nil
	}
	if err != nil {
		return err
	}
	defer e.forgetRemote()
	return explainS3(cl.RemoveObject(ctx, c.Bucket, objectKey(c, site, file), minio.RemoveObjectOptions{}))
}

// pruneRemote keeps the newest keep off-site backups of a site.
func (e *Engine) pruneRemote(ctx context.Context, site string, logf Logf) {
	_, c, err := e.s3(ctx)
	if err != nil {
		return
	}
	e.forgetRemote()
	all, err := e.remoteBackups(ctx)
	if err != nil {
		return
	}
	var mine []model.Backup
	for _, b := range all {
		if b.Site == site {
			mine = append(mine, b)
		}
	}
	store.SortBackups(mine)
	n := 0
	for _, b := range mine {
		if strings.HasSuffix(b.File, "-final.tar.gz") {
			continue
		}
		n++
		if n > c.Keep {
			if err := e.deleteRemote(ctx, site, b.File); err != nil {
				logf("Could not delete old off-site backup %s: %v", b.File, err)
			}
		}
	}
}
