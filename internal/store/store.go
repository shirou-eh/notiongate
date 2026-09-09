// Package store persists accounts, usage counters and request logs in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/shirou-eh/notiongate/internal/model"
)

var ErrNotFound = errors.New("not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	// _pragma busy_timeout guards against transient SQLITE_BUSY.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // sqlite: avoid concurrent write contention
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id             TEXT PRIMARY KEY,
	label          TEXT NOT NULL DEFAULT '',
	token_v2       TEXT NOT NULL,
	user_id        TEXT NOT NULL DEFAULT '',
	space_id       TEXT NOT NULL DEFAULT '',
	space_view_id  TEXT NOT NULL DEFAULT '',
	space_name     TEXT NOT NULL DEFAULT '',
	email          TEXT NOT NULL DEFAULT '',
	status         TEXT NOT NULL DEFAULT 'active',
	models         TEXT NOT NULL DEFAULT '[]',
	limit_req      INTEGER NOT NULL DEFAULT 0,
	window_type    TEXT NOT NULL DEFAULT 'month',
	rotate_at      REAL NOT NULL DEFAULT 0.8,
	proxy          TEXT NOT NULL DEFAULT '',
	base_url       TEXT NOT NULL DEFAULT '',
	cooldown_until TEXT NOT NULL DEFAULT '',
	last_error     TEXT NOT NULL DEFAULT '',
	last_used      TEXT NOT NULL DEFAULT '',
	last_check     TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS counters (
	account_id TEXT NOT NULL,
	window_key TEXT NOT NULL,
	req_count  INTEGER NOT NULL DEFAULT 0,
	in_tokens  INTEGER NOT NULL DEFAULT 0,
	out_tokens INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account_id, window_key)
);
CREATE TABLE IF NOT EXISTS requests (
	id         TEXT PRIMARY KEY,
	ts         TEXT NOT NULL,
	account_id TEXT NOT NULL,
	model      TEXT NOT NULL DEFAULT '',
	protocol   TEXT NOT NULL DEFAULT '',
	in_tokens  INTEGER NOT NULL DEFAULT 0,
	out_tokens INTEGER NOT NULL DEFAULT 0,
	latency_ms INTEGER NOT NULL DEFAULT 0,
	status     TEXT NOT NULL DEFAULT '',
	err_code   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests (ts);
CREATE INDEX IF NOT EXISTS idx_requests_account ON requests (account_id, ts);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// Tolerant column migrations for DBs created before these existed.
	s.db.Exec(`ALTER TABLE accounts ADD COLUMN base_url TEXT NOT NULL DEFAULT ''`)      //nolint:errcheck // duplicate column on fresh DBs
	s.db.Exec(`ALTER TABLE accounts ADD COLUMN space_view_id TEXT NOT NULL DEFAULT ''`) //nolint:errcheck // duplicate column on fresh DBs
	return nil
}

func tm(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func pt(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

const accountCols = `id,label,token_v2,user_id,space_id,space_view_id,space_name,email,status,models,
limit_req,window_type,rotate_at,proxy,base_url,cooldown_until,last_error,last_used,last_check,created_at`

func scanAccount(row interface{ Scan(...any) error }) (model.Account, error) {
	var a model.Account
	var models, cooldown, lastUsed, lastCheck, created string
	err := row.Scan(&a.ID, &a.Label, &a.TokenV2, &a.UserID, &a.SpaceID, &a.SpaceViewID, &a.SpaceName,
		&a.Email, &a.Status, &models, &a.LimitReq, &a.WindowType, &a.RotateAt, &a.Proxy,
		&a.BaseURL, &cooldown, &a.LastError, &lastUsed, &lastCheck, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, fmt.Errorf("scan account: %w", err)
	}
	a.Models = decodeModels(models)
	a.CooldownUntil = pt(cooldown)
	a.LastUsed = pt(lastUsed)
	a.LastCheck = pt(lastCheck)
	a.CreatedAt = pt(created)
	return a, nil
}

// AddAccount inserts a new account.
func (s *Store) AddAccount(a model.Account) error {
	_, err := s.db.Exec(
		`INSERT INTO accounts (`+accountCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Label, a.TokenV2, a.UserID, a.SpaceID, a.SpaceViewID, a.SpaceName, a.Email,
		a.Status, encodeModels(a.Models), a.LimitReq, a.WindowType, a.RotateAt, a.Proxy, a.BaseURL,
		tm(a.CooldownUntil), a.LastError, tm(a.LastUsed), tm(a.LastCheck), tm(a.CreatedAt))
	if err != nil {
		return fmt.Errorf("add account: %w", err)
	}
	return nil
}

// UpdateAccount overwrites all mutable fields of an account.
func (s *Store) UpdateAccount(a model.Account) error {
	res, err := s.db.Exec(
		`UPDATE accounts SET label=?,token_v2=?,user_id=?,space_id=?,space_view_id=?,space_name=?,email=?,status=?,
		 models=?,limit_req=?,window_type=?,rotate_at=?,proxy=?,base_url=?,cooldown_until=?,last_error=?,
		 last_used=?,last_check=? WHERE id=?`,
		a.Label, a.TokenV2, a.UserID, a.SpaceID, a.SpaceViewID, a.SpaceName, a.Email, a.Status,
		encodeModels(a.Models), a.LimitReq, a.WindowType, a.RotateAt, a.Proxy, a.BaseURL,
		tm(a.CooldownUntil), a.LastError, tm(a.LastUsed), tm(a.LastCheck), a.ID)
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateStatus sets only the status and last_error columns (capped).
func (s *Store) UpdateStatus(id, status, lastError string) error {
	if len(lastError) > 500 {
		lastError = lastError[:500] + "…(truncated)"
	}
	_, err := s.db.Exec(`UPDATE accounts SET status=?, last_error=? WHERE id=?`, status, lastError, id)
	if err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// DeleteAccount removes an account and its counters.
func (s *Store) DeleteAccount(id string) error {
	res, err := s.db.Exec(`DELETE FROM accounts WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.db.Exec(`DELETE FROM counters WHERE account_id=?`, id) //nolint:errcheck // best effort
	return nil
}

// ListAccounts returns all accounts ordered by creation time.
func (s *Store) ListAccounts() ([]model.Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	var out []model.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAccount fetches one account by id.
func (s *Store) GetAccount(id string) (model.Account, error) {
	row := s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id=?`, id)
	return scanAccount(row)
}

// CountAccounts returns the number of stored accounts.
func (s *Store) CountAccounts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&n)
	return n, err
}

// UpsertCounter adds deltas to the usage counter of (account, window).
// Retries on transient SQLITE_BUSY (external readers/backups) so quota
// accounting does not silently fail open.
func (s *Store) UpsertCounter(accountID, windowKey string, reqs, inTok, outTok int64) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		_, err = s.db.Exec(`INSERT INTO counters (account_id,window_key,req_count,in_tokens,out_tokens)
			VALUES (?,?,?,?,?)
			ON CONFLICT(account_id,window_key) DO UPDATE SET
			req_count=req_count+excluded.req_count,
			in_tokens=in_tokens+excluded.in_tokens,
			out_tokens=out_tokens+excluded.out_tokens`,
			accountID, windowKey, reqs, inTok, outTok)
		if err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
	}
	return fmt.Errorf("upsert counter: %w", err)
}

// GetCounter reads the current counter for a window.
func (s *Store) GetCounter(accountID, windowKey string) (model.Counter, error) {
	var c model.Counter
	err := s.db.QueryRow(
		`SELECT account_id,window_key,req_count,in_tokens,out_tokens FROM counters
		 WHERE account_id=? AND window_key=?`, accountID, windowKey).
		Scan(&c.AccountID, &c.WindowKey, &c.ReqCount, &c.InTokens, &c.OutTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Counter{AccountID: accountID, WindowKey: windowKey}, nil
	}
	return c, err
}

// InsertRequest appends one request log row.
func (s *Store) InsertRequest(r model.RequestLog) error {
	_, err := s.db.Exec(
		`INSERT INTO requests (id,ts,account_id,model,protocol,in_tokens,out_tokens,latency_ms,status,err_code)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.ID, tm(r.TS), r.AccountID, r.Model, r.Protocol, r.InTokens, r.OutTokens, r.LatencyMS, r.Status, r.ErrCode)
	if err != nil {
		return fmt.Errorf("insert request: %w", err)
	}
	return nil
}

// Stats is an aggregate over request logs.
type Stats struct {
	Requests  int64 `json:"requests"`
	InTokens  int64 `json:"in_tokens"`
	OutTokens int64 `json:"out_tokens"`
	Errors    int64 `json:"errors"`
}

// PruneRequests deletes request logs older than the cutoff.
func (s *Store) PruneRequests(olderThan time.Time) error {
	_, err := s.db.Exec(`DELETE FROM requests WHERE ts < ?`, tm(olderThan))
	if err != nil {
		return fmt.Errorf("prune requests: %w", err)
	}
	return nil
}

// StatsSince aggregates requests since the given time.
func (s *Store) StatsSince(since time.Time) (Stats, error) {
	var st Stats
	err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(in_tokens),0), COALESCE(SUM(out_tokens),0),
		        COALESCE(SUM(CASE WHEN status<>'ok' THEN 1 ELSE 0 END),0)
		 FROM requests WHERE ts>=?`, tm(since)).
		Scan(&st.Requests, &st.InTokens, &st.OutTokens, &st.Errors)
	if errors.Is(err, sql.ErrNoRows) {
		return Stats{}, nil
	}
	return st, err
}

func encodeModels(m []string) string {
	if len(m) == 0 {
		return "[]"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeModels(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
