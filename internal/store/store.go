// Package store keeps characters between sessions in a SQLite file.
//
// The driver is modernc.org/sqlite, a pure Go implementation: the server
// stays a single static binary with no libc dependency, which keeps the
// container image small and the deployment boring.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"hollowmere/internal/game"
)

// Store is the character database.
type Store struct {
	db   *sql.DB
	path string
}

const schemaVersion = 1

// Open creates or opens the database file and applies the schema.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	// WAL keeps readers from blocking the writer; a busy timeout absorbs
	// the rare overlap between a save and the periodic autosave.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	// One writer at a time: SQLite serialises writes anyway, and this
	// avoids "database is locked" under load.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			k TEXT PRIMARY KEY,
			v TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS players (
			key          TEXT PRIMARY KEY,
			name         TEXT NOT NULL,
			name_lower   TEXT NOT NULL UNIQUE,
			data         TEXT NOT NULL,
			play_seconds INTEGER NOT NULL DEFAULT 0,
			created_at   INTEGER NOT NULL,
			last_seen    INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS players_last_seen ON players (last_seen DESC)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("store: schema: %w", err)
		}
	}
	_, err := s.db.Exec(`INSERT INTO meta (k, v) VALUES ('schema_version', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, schemaVersion)
	if err != nil {
		return fmt.Errorf("store: schema: %w", err)
	}
	return nil
}

// Close flushes and closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is the database file, for logs and backups.
func (s *Store) Path() string { return s.path }

// LoadPlayer returns the character behind a resume key, or nil when the key
// is unknown.
func (s *Store) LoadPlayer(key string) (*game.StoredPlayer, error) {
	var (
		name    string
		data    string
		played  int64
		created int64
		seen    int64
	)
	err := s.db.QueryRow(
		`SELECT name, data, play_seconds, created_at, last_seen FROM players WHERE key = ?`, key,
	).Scan(&name, &data, &played, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: load: %w", err)
	}
	sp := &game.StoredPlayer{Key: key, Name: name, Played: time.Duration(played) * time.Second}
	if err := json.Unmarshal([]byte(data), &sp.Data); err != nil {
		return nil, fmt.Errorf("store: load %s: %w", name, err)
	}
	return sp, nil
}

// NameOwner returns the key owning a name (case-insensitive), or "".
func (s *Store) NameOwner(name string) (string, error) {
	var key string
	err := s.db.QueryRow(`SELECT key FROM players WHERE name_lower = ?`, strings.ToLower(name)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: name lookup: %w", err)
	}
	return key, nil
}

// SavePlayer writes a character, creating its row on first save.
func (s *Store) SavePlayer(p *game.StoredPlayer) error {
	data, err := json.Marshal(p.Data)
	if err != nil {
		return fmt.Errorf("store: save %s: %w", p.Name, err)
	}
	now := time.Now().Unix()
	_, err = s.db.Exec(`
		INSERT INTO players (key, name, name_lower, data, play_seconds, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name         = excluded.name,
			name_lower   = excluded.name_lower,
			data         = excluded.data,
			play_seconds = excluded.play_seconds,
			last_seen    = excluded.last_seen`,
		p.Key, p.Name, strings.ToLower(p.Name), string(data), int64(p.Played.Seconds()), now, now)
	if err != nil {
		return fmt.Errorf("store: save %s: %w", p.Name, err)
	}
	return nil
}

// Count returns how many characters exist.
func (s *Store) Count() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count: %w", err)
	}
	return n, nil
}

// Entry is one line of the leaderboard.
type Entry struct {
	Name     string
	Played   time.Duration
	LastSeen time.Time
	Quests   int
}

// Recent lists the characters seen most recently.
func (s *Store) Recent(limit int) ([]Entry, error) {
	rows, err := s.db.Query(
		`SELECT name, data, play_seconds, last_seen FROM players ORDER BY last_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			name   string
			data   string
			played int64
			seen   int64
		)
		if err := rows.Scan(&name, &data, &played, &seen); err != nil {
			return nil, fmt.Errorf("store: recent: %w", err)
		}
		e := Entry{Name: name, Played: time.Duration(played) * time.Second, LastSeen: time.Unix(seen, 0)}
		var save game.PlayerSave
		if json.Unmarshal([]byte(data), &save) == nil {
			for _, q := range save.Quests {
				if q.Status == "completed" {
					e.Quests++
				}
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Backup writes a consistent copy of the database, safe to run while the
// server is playing (SQLite's VACUUM INTO takes care of the locking).
func (s *Store) Backup(dest string) error {
	if dir := filepath.Dir(dest); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("store: backup: %w", err)
		}
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("store: backup: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return fmt.Errorf("store: backup: %w", err)
	}
	return nil
}
