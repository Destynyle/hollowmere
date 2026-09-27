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

// schemaVersion history:
//
//	1: players (key, name, data, play time)
//	2: leaderboard columns (level, xp, quests, kills), filled from data
//	3: moderation (admins, sanctions, modlog) and players.last_ip
const schemaVersion = 3

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
	var current int
	err := s.db.QueryRow(`SELECT CAST(v AS INTEGER) FROM meta WHERE k = 'schema_version'`).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: schema: %w", err)
	}
	if current < 2 {
		if err := s.migrateLeaderboard(); err != nil {
			return err
		}
	}
	if current < 3 {
		if err := s.migrateModeration(); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`INSERT INTO meta (k, v) VALUES ('schema_version', ?)
		ON CONFLICT(k) DO UPDATE SET v = excluded.v`, schemaVersion)
	if err != nil {
		return fmt.Errorf("store: schema: %w", err)
	}
	return nil
}

// migrateLeaderboard adds the leaderboard columns and fills them from the
// saved characters, in one transaction.
func (s *Store) migrateLeaderboard() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	defer tx.Rollback()
	has := map[string]bool{}
	rows, err := tx.Query(`SELECT name FROM pragma_table_info('players')`)
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			rows.Close()
			return fmt.Errorf("store: migrate: %w", err)
		}
		has[col] = true
	}
	rows.Close()
	for _, col := range []string{"level", "xp", "quests", "kills"} {
		if has[col] {
			continue
		}
		def := "0"
		if col == "level" {
			def = "1"
		}
		if _, err := tx.Exec(`ALTER TABLE players ADD COLUMN ` + col + ` INTEGER NOT NULL DEFAULT ` + def); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS players_rank ON players (level DESC, xp DESC, quests DESC, kills DESC)`); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	type row struct {
		key  string
		save game.PlayerSave
	}
	var all []row
	rs, err := tx.Query(`SELECT key, data FROM players`)
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	for rs.Next() {
		var r row
		var data string
		if err := rs.Scan(&r.key, &data); err != nil {
			rs.Close()
			return fmt.Errorf("store: migrate: %w", err)
		}
		if json.Unmarshal([]byte(data), &r.save) == nil {
			all = append(all, r)
		}
	}
	rs.Close()
	for _, r := range all {
		level, xp, quests, kills := rankOf(&r.save)
		if _, err := tx.Exec(`UPDATE players SET level = ?, xp = ?, quests = ?, kills = ? WHERE key = ?`,
			level, xp, quests, kills, r.key); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}
	return tx.Commit()
}

// rankOf extracts the leaderboard figures of a save.
func rankOf(save *game.PlayerSave) (level, xp, quests, kills int) {
	level = save.Level
	if level < 1 {
		level = 1
	}
	for _, q := range save.Quests {
		if q.Status == "completed" {
			quests++
		}
	}
	return level, save.XP, quests, save.Kills
}

// Top returns the best characters: by level, then experience, quests
// completed and enemies defeated.
func (s *Store) Top(limit int) ([]game.TopEntry, error) {
	rows, err := s.db.Query(`SELECT name, level, xp, quests, kills FROM players
		ORDER BY level DESC, xp DESC, quests DESC, kills DESC, name_lower ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: top: %w", err)
	}
	defer rows.Close()
	var out []game.TopEntry
	for rows.Next() {
		var e game.TopEntry
		if err := rows.Scan(&e.Name, &e.Level, &e.XP, &e.Quests, &e.Kills); err != nil {
			return nil, fmt.Errorf("store: top: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Verify runs SQLite's quick integrity check and returns how many
// characters the database holds.
func (s *Store) Verify() (int, error) {
	var res string
	if err := s.db.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil {
		return 0, fmt.Errorf("store: verify: %w", err)
	}
	if res != "ok" {
		return 0, fmt.Errorf("store: verify: %s", res)
	}
	return s.Count()
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
	level, xp, quests, kills := rankOf(&p.Data)
	_, err = s.db.Exec(`
		INSERT INTO players (key, name, name_lower, data, play_seconds, created_at, last_seen, level, xp, quests, kills, last_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name         = excluded.name,
			name_lower   = excluded.name_lower,
			data         = excluded.data,
			play_seconds = excluded.play_seconds,
			last_seen    = excluded.last_seen,
			level        = excluded.level,
			xp           = excluded.xp,
			quests       = excluded.quests,
			kills        = excluded.kills,
			last_ip      = CASE WHEN excluded.last_ip = '' THEN players.last_ip ELSE excluded.last_ip END`,
		p.Key, p.Name, strings.ToLower(p.Name), string(data), int64(p.Played.Seconds()), now, now, level, xp, quests, kills, p.IP)
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
