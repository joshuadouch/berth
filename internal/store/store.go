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

// ErrNameTaken is returned when a username is already in use.
var ErrNameTaken = errors.New("that username is taken")

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
CREATE TABLE IF NOT EXISTS accounts (id TEXT PRIMARY KEY, data TEXT NOT NULL);
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
	if err := addColumns(db, "users", map[string]string{
		"role":       "TEXT NOT NULL DEFAULT ''",
		"account_id": "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// A job remembers its site's account, so the account can still follow
	// it after the site is gone (deleting a site removes its record).
	if err := addColumns(db, "jobs", map[string]string{
		"account_id": "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Jobs that were running when Berth stopped will never finish.
	_, _ = db.Exec(`UPDATE jobs SET status='failed', error='Berth restarted while this was running', finished=? WHERE status='running'`, now())
	return &Store{db: db}, nil
}

// addColumns adds missing columns to a table.
func addColumns(db *sql.DB, table string, cols map[string]string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	rows.Close()
	names := make([]string, 0, len(cols))
	for n := range cols {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if have[n] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + n + ` ` + cols[n]); err != nil {
			return err
		}
	}
	return nil
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

// DeleteKV removes a stored value.
func (s *Store) DeleteKV(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key=?`, key)
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

// EventsIn returns recent events of the named sites, newest first.
func (s *Store) EventsIn(ctx context.Context, sites []string, limit int) ([]model.Event, error) {
	out := []model.Event{}
	if len(sites) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(sites)+1)
	for _, n := range sites {
		args = append(args, n)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id,time,level,kind,site,message FROM events WHERE site IN (`+marks(len(sites))+`) ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// Jobs ------------------------------------------------------------------------

// StartJob records a new running job.
func (s *Store) StartJob(ctx context.Context, site, account, kind, title string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO jobs(site,account_id,kind,title,status,started) VALUES(?,?,?,?,?,?)`, site, account, kind, title, "running", now())
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
	err := sc.Scan(&j.ID, &j.Site, &j.AccountID, &j.Kind, &j.Title, &j.Status, &j.Log, &st, &fin, &j.Error)
	j.Started, j.Finished = parseTime(st), parseTime(fin)
	return j, err
}

const jobCols = `id,site,account_id,kind,title,status,log,started,finished,error`

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

// JobsIn lists recent jobs of the named sites, newest first, without logs.
func (s *Store) JobsIn(ctx context.Context, sites []string, limit int) ([]model.Job, error) {
	out := []model.Job{}
	if len(sites) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(sites)+1)
	for _, n := range sites {
		args = append(args, n)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+strings.Replace(jobCols, "log", "''", 1)+` FROM jobs WHERE site IN (`+marks(len(sites))+`) ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,username,email,password_hash,totp_secret,totp_enabled,role,account_id) VALUES(?,?,?,?,?,?,?,?)
	  ON CONFLICT(id) DO UPDATE SET username=excluded.username, email=excluded.email, password_hash=excluded.password_hash,
	  totp_secret=excluded.totp_secret, totp_enabled=excluded.totp_enabled, role=excluded.role, account_id=excluded.account_id`,
		u.ID, u.Username, u.Email, u.PasswordHash, u.TOTPSecret, boolInt(u.TOTPEnabled), u.Role, u.AccountID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrNameTaken
	}
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const userCols = `id,username,email,password_hash,totp_secret,totp_enabled,role,account_id`

func scanUser(sc interface{ Scan(...any) error }) (model.User, error) {
	var u model.User
	var totp int
	err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.TOTPSecret, &totp, &u.Role, &u.AccountID)
	u.TOTPEnabled = totp == 1
	return u, err
}

func (s *Store) userWhere(ctx context.Context, where string, arg any) (model.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// Users lists every user, optionally of one account, oldest first.
func (s *Store) Users(ctx context.Context, accountID string) ([]model.User, error) {
	q, args := `SELECT `+userCols+` FROM users`, []any{}
	if accountID != "" {
		q += ` WHERE account_id=?`
		args = append(args, accountID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY rowid`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// DeleteUser removes a user and their sessions.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	s.DeleteUserSessions(ctx, id)
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// CountAdmins returns how many admin users exist.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role=?`, model.RoleAdmin).Scan(&n)
	return n, err
}

// Accounts ----------------------------------------------------------------------

// SaveAccount inserts or replaces an account.
func (s *Store) SaveAccount(ctx context.Context, a model.Account) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO accounts(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, a.ID, string(b))
	return err
}

// Account loads one account.
func (s *Store) Account(ctx context.Context, id string) (model.Account, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM accounts WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Account{}, ErrNotFound
	}
	if err != nil {
		return model.Account{}, err
	}
	var a model.Account
	return a, json.Unmarshal([]byte(data), &a)
}

// Accounts lists every account, oldest first.
func (s *Store) Accounts(ctx context.Context) ([]model.Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM accounts ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Account{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		var a model.Account
		if err := json.Unmarshal([]byte(d), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteAccount removes an account and its users.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	users, err := s.Users(ctx, id)
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := s.DeleteUser(ctx, u.ID); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id=?`, id)
	return err
}

// MigrateAccounts moves a panel from before accounts existed: its users
// become admins of one account that owns every site.
func (s *Store) MigrateAccounts(ctx context.Context) error {
	users, err := s.Users(ctx, "")
	if err != nil || len(users) == 0 {
		return err
	}
	accounts, err := s.Accounts(ctx)
	if err != nil {
		return err
	}
	var home string
	if len(accounts) > 0 {
		home = accounts[0].ID
	}
	for _, u := range users {
		if u.AccountID != "" && u.Role != "" {
			continue
		}
		if home == "" {
			a := model.Account{ID: NewID(), Name: users[0].Username, Email: users[0].Email, CreatedAt: time.Now().UTC()}
			if err := s.SaveAccount(ctx, a); err != nil {
				return err
			}
			home = a.ID
		}
		if u.AccountID == "" {
			u.AccountID = home
		}
		if u.Role == "" {
			u.Role = model.RoleAdmin
		}
		if err := s.SaveUser(ctx, u); err != nil {
			return err
		}
	}
	if home == "" {
		return nil
	}
	sites, err := s.Sites(ctx)
	if err != nil {
		return err
	}
	for _, site := range sites {
		if site.AccountID == "" {
			site.AccountID = home
			if err := s.SaveSite(ctx, site); err != nil {
				return err
			}
		}
	}
	return nil
}

// UserByName finds a user by username.
func (s *Store) UserByName(ctx context.Context, name string) (model.User, error) {
	return s.userWhere(ctx, "username=?", name)
}

// UserByID finds a user by ID.
func (s *Store) UserByID(ctx context.Context, id string) (model.User, error) {
	return s.userWhere(ctx, "id=?", id)
}

// FirstUser returns the oldest admin.
func (s *Store) FirstUser(ctx context.Context) (model.User, error) {
	return s.userWhere(ctx, "role='admin' ORDER BY rowid LIMIT ?", 1)
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

// Plans lists the hosting plans.
func (s *Store) Plans(ctx context.Context) ([]model.Plan, error) {
	var ps []model.Plan
	if err := s.GetKV(ctx, "plans", &ps); err != nil {
		return nil, err
	}
	if ps == nil {
		ps = []model.Plan{}
	}
	return ps, nil
}

// Plan loads one hosting plan.
func (s *Store) Plan(ctx context.Context, id string) (model.Plan, error) {
	ps, err := s.Plans(ctx)
	if err != nil {
		return model.Plan{}, err
	}
	for _, p := range ps {
		if p.ID == id {
			return p, nil
		}
	}
	return model.Plan{}, ErrNotFound
}

// SavePlan adds or replaces a hosting plan.
func (s *Store) SavePlan(ctx context.Context, p model.Plan) error {
	ps, err := s.Plans(ctx)
	if err != nil {
		return err
	}
	for i := range ps {
		if ps[i].ID == p.ID {
			ps[i] = p
			return s.SetKV(ctx, "plans", ps)
		}
	}
	return s.SetKV(ctx, "plans", append(ps, p))
}

// DeletePlan removes a hosting plan.
func (s *Store) DeletePlan(ctx context.Context, id string) error {
	ps, err := s.Plans(ctx)
	if err != nil {
		return err
	}
	out := ps[:0]
	for _, p := range ps {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return s.SetKV(ctx, "plans", out)
}

// AccountPlan returns the plan of an account with its add-ons folded in,
// and false when it has neither.
func (s *Store) AccountPlan(ctx context.Context, accountID string) (model.Plan, bool) {
	a, err := s.Account(ctx, accountID)
	if err != nil {
		return model.Plan{}, false
	}
	var p model.Plan
	ok := false
	if a.PlanID != "" {
		if p, err = s.Plan(ctx, a.PlanID); err == nil {
			ok = true
		}
	}
	for _, id := range a.AddonPlans {
		ap, err := s.Plan(ctx, id)
		if err != nil {
			continue
		}
		if !ok {
			p, ok = ap, true
			continue
		}
		p = p.WithAddon(ap)
	}
	return p, ok
}
