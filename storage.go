package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/minio/minio-go/v7/pkg/s3utils"
)

// s3Config is one S3-compatible storage added on the settings page.
// SecretKey never leaves the server.
type s3Config struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	Bucket     string `json:"bucket"`
	Prefix     string `json:"prefix"`
	AccessKey  string `json:"accessKey"`
	SecretKey  string `json:"-"`
	Addressing string `json:"addressing"`
	ListV1     bool   `json:"listV1"`
}

const (
	addressingAuto    = "auto"
	addressingPath    = "path"
	addressingVirtual = "virtual"
)

var errStorageNotFound = errors.New("storage not found")

// userError is a validation message safe to show on the settings page.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

// normalize trims input and fills defaults. An endpoint without a scheme uses
// HTTPS; an empty region is detected from the service when first used.
func (c s3Config) normalize() (s3Config, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Region = strings.TrimSpace(c.Region)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)
	c.Addressing = strings.TrimSpace(c.Addressing)

	endpoint, err := normalizeEndpoint(c.Endpoint)
	if err != nil {
		return c, err
	}
	c.Endpoint = endpoint
	if c.Bucket == "" {
		return c, invalid("请填写 Bucket")
	}
	if err := s3utils.CheckValidBucketName(c.Bucket); err != nil {
		return c, invalid("Bucket 名称不合法：%s", c.Bucket)
	}
	prefix, err := cleanObjectPrefix(c.Prefix)
	if err != nil {
		return c, invalid("前缀不能包含 .. 或多余的斜杠")
	}
	c.Prefix = strings.TrimSuffix(prefix, "/")
	if strings.ContainsAny(c.Region, " /\\") {
		return c, invalid("Region 格式不正确")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return c, invalid("请填写 Access Key 和 Secret Key")
	}
	switch c.Addressing {
	case "":
		c.Addressing = addressingAuto
	case addressingAuto, addressingPath, addressingVirtual:
	default:
		return c, invalid("寻址方式只能是自动、路径或虚拟主机")
	}
	if c.Name == "" {
		c.Name = c.Bucket
	}
	if utf8.RuneCountInString(c.Name) > 40 {
		return c, invalid("名称最多 40 个字")
	}
	return c, nil
}

func normalizeEndpoint(raw string) (string, error) {
	if raw == "" {
		return "", invalid("请填写 Endpoint")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", invalid("Endpoint 格式不正确，例如 https://s3.example.com")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", invalid("Endpoint 只能使用 http:// 或 https://")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", invalid("Endpoint 只填协议、域名和端口，bucket 和路径分别填写在下面")
	}
	return u.Scheme + "://" + u.Host, nil
}

func (s *store) migrateStorages() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS storages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  region TEXT NOT NULL DEFAULT '',
  bucket TEXT NOT NULL,
  prefix TEXT NOT NULL DEFAULT '',
  access_key TEXT NOT NULL,
  secret_key TEXT NOT NULL,
  addressing TEXT NOT NULL DEFAULT 'auto',
  list_v1 INTEGER NOT NULL DEFAULT 0
)`)
	return err
}

const storageColumns = `id, name, endpoint, region, bucket, prefix, access_key, secret_key, addressing, list_v1`

func scanStorage(row interface{ Scan(...any) error }) (s3Config, error) {
	var c s3Config
	var listV1 int
	err := row.Scan(&c.ID, &c.Name, &c.Endpoint, &c.Region, &c.Bucket, &c.Prefix, &c.AccessKey, &c.SecretKey, &c.Addressing, &listV1)
	c.ListV1 = listV1 != 0
	return c, err
}

func (s *store) listStorages() ([]s3Config, error) {
	rows, err := s.db.Query(`SELECT ` + storageColumns + ` FROM storages ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []s3Config
	for rows.Next() {
		c, err := scanStorage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *store) getStorage(id int64) (s3Config, error) {
	c, err := scanStorage(s.db.QueryRow(`SELECT `+storageColumns+` FROM storages WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return c, errStorageNotFound
	}
	return c, err
}

// saveStorage inserts when c.ID is 0, otherwise updates. The same bucket and
// prefix on the same endpoint may only be added once.
func (s *store) saveStorage(c s3Config) (int64, error) {
	var dup int64
	err := s.db.QueryRow(`SELECT id FROM storages WHERE endpoint = ? AND bucket = ? AND prefix = ? AND id != ?`,
		c.Endpoint, c.Bucket, c.Prefix, c.ID).Scan(&dup)
	if err == nil {
		return 0, invalid("这个 Bucket 和前缀已经添加过了")
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	listV1 := 0
	if c.ListV1 {
		listV1 = 1
	}
	if c.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO storages (name, endpoint, region, bucket, prefix, access_key, secret_key, addressing, list_v1) VALUES (?,?,?,?,?,?,?,?,?)`,
			c.Name, c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKey, c.SecretKey, c.Addressing, listV1)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.Exec(`UPDATE storages SET name=?, endpoint=?, region=?, bucket=?, prefix=?, access_key=?, secret_key=?, addressing=?, list_v1=? WHERE id=?`,
		c.Name, c.Endpoint, c.Region, c.Bucket, c.Prefix, c.AccessKey, c.SecretKey, c.Addressing, listV1, c.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errStorageNotFound
	}
	return c.ID, nil
}

func (s *store) deleteStorage(id int64) error {
	res, err := s.db.Exec(`DELETE FROM storages WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errStorageNotFound
	}
	return nil
}
