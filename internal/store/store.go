// Package store persists Berth state in a single SQLite file.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joshuadouch/berth/internal/model"
	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sites (id TEXT PRIMARY KEY, data TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, username TEXT UNIQUE NOT NULL, email TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '', totp_secret TEXT NOT NULL DEFAULT '', totp_enabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, user_id TEXT NOT NULL, expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, time TEXT NOT NULL, level TEXT NOT NULL, kind TEXT NOT NULL,
  site TEXT NOT NULL DEFAULT '', message TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, site TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL,
  status TEXT NOT NULL, log TEXT NOT NULL DEFAULT '', started TEXT NOT NULL, finished TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '');
`

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + statsSchema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Jobs that were running when Berth stopped will never finish.
	_, _ = db.Exec(`UPDATE jobs SET status='failed', error='Berth restarted while this was running', finished=? WHERE status='running'`, now())
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// NewID returns a short random identifier.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Sites ---------------------------------------------------------------------

// SaveSite inserts or replaces a site.
func (s *Store) SaveSite(ctx context.Context, site model.Site) error {
	b, err := json.Marshal(site)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sites(id,data,updated_at) VALUES(?,?,?)
	  ON CONFLICT(id) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`, site.ID, string(b), now())
	return err
}

// Site loads one site.
func (s *Store) Site(ctx context.Context, id string) (model.Site, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM sites WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Site{}, ErrNotFound
	}
	if err != nil {
		return model.Site{}, err
	}
	var site model.Site
	return site, json.Unmarshal([]byte(data), &site)
}

// Sites lists every site by name.
func (s *Store) Sites(ctx context.Context) ([]model.Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM sites ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Site{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		var site model.Site
		if err := json.Unmarshal([]byte(d), &site); err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// DeleteSite removes a site record.
func (s *Store) DeleteSite(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE id=?`, id)
	return err
}

// Key/value -------------------------------------------------------------------

// GetKV loads a JSON value; a missing key leaves v unchanged.
func (s *Store) GetKV(ctx context.Context, key string, v any) error {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key=?`, key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(data), v)
}

// SetKV stores a JSON value.
func (s *Store) SetKV(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(b))
	return err
}

// Settings returns the panel settings with defaults filled in.
func (s *Store) Settings(ctx context.Context) (model.Settings, error) {
	st := model.Settings{BackupEnabled: true}
	if err := s.GetKV(ctx, "settings", &st); err != nil {
		return st, err
	}
	_ = st.Normalize()
	return st, nil
}

// SetSettings stores the panel settings.
func (s *Store) SetSettings(ctx context.Context, st model.Settings) error {
	return s.SetKV(ctx, "settings", st)
}

// Events ----------------------------------------------------------------------

// AddEvent records an activity log entry.
func (s *Store) AddEvent(ctx context.Context, level, kind, site, msg string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO events(time,level,kind,site,message) VALUES(?,?,?,?,?)`, now(), level, kind, site, msg)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM events WHERE id <= (SELECT MAX(id) FROM events) - 5000`)
}

// Events returns recent events, newest first, optionally for one site.
func (s *Store) Events(ctx context.Context, site string, limit int) ([]model.Event, error) {
	q := `SELECT id,time,level,kind,site,message FROM events`
	args := []any{}
	if site != "" {
		q += ` WHERE site=?`
		args = append(args, site)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var e model.Event
		var t string
		if err := rows.Scan(&e.ID, &t, &e.Level, &e.Kind, &e.Site, &e.Message); err != nil {
			return nil, err
		}
		e.Time = parseTime(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Jobs ------------------------------------------------------------------------

// StartJob records a new running job.
func (s *Store) StartJob(ctx context.Context, site, kind, title string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO jobs(site,kind,title,status,started) VALUES(?,?,?,?,?)`, site, kind, title, "running", now())
	if err != nil {
		return 0, err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id <= (SELECT MAX(id) FROM jobs) - 500`)
	return res.LastInsertId()
}

// AppendJobLog adds a line to a job's log.
func (s *Store) AppendJobLog(ctx context.Context, id int64, line string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET log = substr(log || ? || char(10), -200000) WHERE id=?`, line, id)
}

// FinishJob marks a job done.
func (s *Store) FinishJob(ctx context.Context, id int64, jobErr error) {
	status, msg := "ok", ""
	if jobErr != nil {
		status, msg = "failed", jobErr.Error()
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE jobs SET status=?, error=?, finished=? WHERE id=?`, status, msg, now(), id)
}

func scanJob(sc interface{ Scan(...any) error }) (model.Job, error) {
	var j model.Job
	var st, fin string
	err := sc.Scan(&j.ID, &j.Site, &j.Kind, &j.Title, &j.Status, &j.Log, &st, &fin, &j.Error)
	j.Started, j.Finished = parseTime(st), parseTime(fin)
	return j, err
}

const jobCols = `id,site,kind,title,status,log,started,finished,error`

// Job loads one job.
func (s *Store) Job(ctx context.Context, id int64) (model.Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// Jobs lists recent jobs, newest first, optionally for one site. Logs are
// left out to keep lists small.
func (s *Store) Jobs(ctx context.Context, site string, limit int) ([]model.Job, error) {
	q := `SELECT ` + strings.Replace(jobCols, "log", "''", 1) + ` FROM jobs`
	args := []any{}
	if site != "" {
		q += ` WHERE site=?`
		args = append(args, site)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Users and sessions ----------------------------------------------------------

// CountUsers returns how many users exist.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// SaveUser inserts or replaces a user.
func (s *Store) SaveUser(ctx context.Context, u model.User) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,totp_secret,totp_enabled) VALUES(?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET username=excluded.username, email=excluded.email, password_hash=excluded.password_hash,
	  totp_secret=excluded.totp_secret, totp_enabled=excluded.totp_enabled`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.TOTPSecret, boolInt(u.TOTPEnabled))
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) userWhere(ctx context.Context, where string, arg any) (model.User, error) {
	var u model.User
	var totp int
	err := s.db.QueryRowContext(ctx, `SELECT id,username,email,password_hash,totp_secret,totp_enabled FROM users WHERE `+where, arg).
		Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.TOTPSecret, &totp)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.TOTPEnabled = totp == 1
	return u, err
}

// UserByName finds a user by username.
func (s *Store) UserByName(ctx context.Context, name string) (model.User, error) {
	return s.userWhere(ctx, "username=?", name)
}

// UserByID finds a user by ID.
func (s *Store) UserByID(ctx context.Context, id string) (model.User, error) {
	return s.userWhere(ctx, "id=?", id)
}

// FirstUser returns the oldest account (the admin).
func (s *Store) FirstUser(ctx context.Context) (model.User, error) {
	return s.userWhere(ctx, "1=1 ORDER BY rowid LIMIT ?", 1)
}

// CreateSession stores a login session.
func (s *Store) CreateSession(ctx context.Context, token, userID string, ttl time.Duration) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(token,user_id,expires_at) VALUES(?,?,?)`,
		token, userID, time.Now().Add(ttl).UTC().Format(time.RFC3339Nano))
	return err
}

// SessionUser returns the user for a valid session token.
func (s *Store) SessionUser(ctx context.Context, token string) (model.User, error) {
	var uid, exp string
	err := s.db.QueryRowContext(ctx, `SELECT user_id,expires_at FROM sessions WHERE token=?`, token).Scan(&uid, &exp)
	if err != nil {
		return model.User{}, ErrNotFound
	}
	if parseTime(exp).Before(time.Now()) {
		s.DeleteSession(ctx, token)
		return model.User{}, ErrNotFound
	}
	return s.UserByID(ctx, uid)
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, token string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token=?`, token)
}

// DeleteUserSessions ends every session of a user.
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
}

// SortBackups orders backups newest first.
func SortBackups(b []model.Backup) {
	sort.Slice(b, func(i, j int) bool { return b[i].Time.After(b[j].Time) })
}
