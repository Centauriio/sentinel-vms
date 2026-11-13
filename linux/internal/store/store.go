// Package store is Sentinel's persistence layer: one SQLite database in the
// data directory plus an install key that encrypts camera passwords at rest.
package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sql.DB
	dataDir string
	aead    cipher.AEAD
	hooks   alarmHooks // new-alarm listeners (the event bus); see hooks.go
}

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
	role          TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS cameras (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	location       TEXT NOT NULL DEFAULT '',
	rtsp_url       TEXT NOT NULL,
	sub_rtsp_url   TEXT NOT NULL DEFAULT '',
	username       TEXT NOT NULL DEFAULT '',
	password_enc   TEXT NOT NULL DEFAULT '',
	recording      INTEGER NOT NULL DEFAULT 1,
	retention_days INTEGER NOT NULL DEFAULT 0,
	sort_order     INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS usb_cameras (
 camera_id TEXT PRIMARY KEY REFERENCES cameras(id) ON DELETE CASCADE,
 instance TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS paired_devices (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	paired_by    TEXT NOT NULL DEFAULT '',
	paired_at    INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	apns_token   TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS alarms (
	id             TEXT PRIMARY KEY,
	camera_id      TEXT NOT NULL DEFAULT '',
	camera_name    TEXT NOT NULL DEFAULT '',
	kind           TEXT NOT NULL,
	title          TEXT NOT NULL,
	detail         TEXT NOT NULL DEFAULT '',
	severity       TEXT NOT NULL,
	state          TEXT NOT NULL,
	owner          TEXT NOT NULL DEFAULT 'Unassigned',
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	last_event_at  INTEGER NOT NULL,
	event_count    INTEGER NOT NULL DEFAULT 1,
	event_id       TEXT NOT NULL DEFAULT '',
	response_log   TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS alarms_open ON alarms(state, last_event_at);
CREATE TABLE IF NOT EXISTS events (
	id          TEXT PRIMARY KEY,
	camera_id   TEXT NOT NULL,
	kind        TEXT NOT NULL,
	score       REAL NOT NULL,
	created_at  INTEGER NOT NULL,
	thumbnail   TEXT NOT NULL DEFAULT '',
	alarm_id    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS events_time ON events(created_at);
CREATE INDEX IF NOT EXISTS events_camera_time ON events(camera_id, created_at);
CREATE INDEX IF NOT EXISTS events_kind_time ON events(kind, created_at);
CREATE TABLE IF NOT EXISTS floorplans (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	image      BLOB NOT NULL,
	mime       TEXT NOT NULL DEFAULT 'image/png',
	width      INTEGER NOT NULL DEFAULT 0,
	height     INTEGER NOT NULL DEFAULT 0,
	sort_order INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS floorplan_pins (
	floorplan_id TEXT NOT NULL REFERENCES floorplans(id) ON DELETE CASCADE,
	camera_id    TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
	x            REAL NOT NULL,
	y            REAL NOT NULL,
	heading      REAL NOT NULL DEFAULT -1,
	PRIMARY KEY (floorplan_id, camera_id)
);
CREATE TABLE IF NOT EXISTS evidence (
	id          TEXT PRIMARY KEY,
	case_id     TEXT NOT NULL UNIQUE,
	alarm_id    TEXT NOT NULL DEFAULT '',
	camera_id   TEXT NOT NULL,
	camera_name TEXT NOT NULL,
	title       TEXT NOT NULL,
	range_label TEXT NOT NULL,
	file        TEXT NOT NULL,
	size        INTEGER NOT NULL,
	sha256      TEXT NOT NULL,
	locked_at   INTEGER NOT NULL,
	locked_by   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
	seq           INTEGER PRIMARY KEY AUTOINCREMENT,
	id            TEXT NOT NULL,
	time          INTEGER NOT NULL,
	user          TEXT NOT NULL,
	area          TEXT NOT NULL,
	action        TEXT NOT NULL,
	detail        TEXT NOT NULL,
	previous_hash TEXT NOT NULL,
	chain_hash    TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alarm_rules (
	id         TEXT PRIMARY KEY,
	position   INTEGER NOT NULL DEFAULT 0,
	body       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS view_groups (
	id         TEXT PRIMARY KEY,
	parent_id  TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL,
	sort_order INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS incidents (
	id         TEXT PRIMARY KEY,
	title      TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'open',
	severity   TEXT NOT NULL DEFAULT 'medium',
	summary    TEXT NOT NULL DEFAULT '',
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS incident_items (
	id          TEXT PRIMARY KEY,
	incident_id TEXT NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	ref_id      TEXT NOT NULL DEFAULT '',
	note        TEXT NOT NULL DEFAULT '',
	added_by    TEXT NOT NULL,
	added_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS incident_items_incident ON incident_items(incident_id, added_at);
CREATE TABLE IF NOT EXISTS roles (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	description TEXT NOT NULL DEFAULT '',
	permissions TEXT NOT NULL DEFAULT '[]',
	builtin     INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS time_profiles (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
	body       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS webhooks (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	url           TEXT NOT NULL,
	secret        TEXT NOT NULL DEFAULT '',
	events        TEXT NOT NULL DEFAULT '[]',
	enabled       INTEGER NOT NULL DEFAULT 1,
	last_status   TEXT NOT NULL DEFAULT '',
	last_fired_at INTEGER NOT NULL DEFAULT 0,
	created_at    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS camera_zones (
	id         TEXT PRIMARY KEY,
	camera_id  TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL DEFAULT 'detect',
	name       TEXT NOT NULL DEFAULT '',
	points     TEXT NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS camera_zones_camera ON camera_zones(camera_id, kind);
CREATE TABLE IF NOT EXISTS bookmarks (
	id          TEXT PRIMARY KEY,
	camera_id   TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
	at_ms       INTEGER NOT NULL,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	title       TEXT NOT NULL DEFAULT '',
	note        TEXT NOT NULL DEFAULT '',
	color       TEXT NOT NULL DEFAULT '',
	created_by  TEXT NOT NULL,
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS bookmarks_camera_time ON bookmarks(camera_id, at_ms);
CREATE TABLE IF NOT EXISTS export_schedules (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	camera_id   TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
	days        TEXT NOT NULL DEFAULT '[]',
	start_hm    TEXT NOT NULL,
	end_hm      TEXT NOT NULL,
	dest        TEXT NOT NULL DEFAULT 'evidence',
	enabled     INTEGER NOT NULL DEFAULT 1,
	created_by  TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	last_run_at INTEGER NOT NULL DEFAULT 0,
	last_status TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS event_embeddings (
	event_id  TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
	model     TEXT NOT NULL,
	dim       INTEGER NOT NULL,
	vec       BLOB NOT NULL,
	camera_id TEXT NOT NULL,
	at_ms     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS event_embeddings_time ON event_embeddings(camera_id, at_ms);
CREATE TABLE IF NOT EXISTS wall_layouts (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	body       TEXT NOT NULL DEFAULT '{}',
	shared     INTEGER NOT NULL DEFAULT 0,
	created_by TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS device_codes (
	id               TEXT PRIMARY KEY,
	user_code_hash   TEXT NOT NULL UNIQUE,
	device_code_hash TEXT NOT NULL UNIQUE,
	device_name      TEXT NOT NULL DEFAULT '',
	status           TEXT NOT NULL DEFAULT 'pending',
	approved_by      TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	expires_at       INTEGER NOT NULL,
	last_poll_at     INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS share_links (
	id          TEXT PRIMARY KEY,
	token_hash  TEXT NOT NULL UNIQUE,
	evidence_id TEXT NOT NULL,
	note        TEXT NOT NULL DEFAULT '',
	expires_at  INTEGER NOT NULL,
	max_uses    INTEGER NOT NULL DEFAULT 0,
	uses        INTEGER NOT NULL DEFAULT 0,
	created_by  TEXT NOT NULL,
	created_at  INTEGER NOT NULL,
	revoked_at  INTEGER NOT NULL DEFAULT 0
);
-- wave16-plates: the plate watchlist (plates.go). events.plate is added by
-- columnAdds; its index is made lazily (plates.go) because this schema runs
-- before the column exists on older databases.
CREATE TABLE IF NOT EXISTS plates (
	id         TEXT PRIMARY KEY,
	plate      TEXT NOT NULL UNIQUE,
	name       TEXT NOT NULL DEFAULT '',
	notes      TEXT NOT NULL DEFAULT '',
	alert      TEXT NOT NULL DEFAULT 'default',
	created_at INTEGER NOT NULL
);
-- wave16-semantic: CLIP vectors of whole event snapshots (semantic.go)
CREATE TABLE IF NOT EXISTS event_semantic (
	event_id  TEXT PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
	model     TEXT NOT NULL,
	dim       INTEGER NOT NULL,
	vec       BLOB NOT NULL,
	camera_id TEXT NOT NULL,
	at_ms     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS event_semantic_time ON event_semantic(camera_id, at_ms);
-- wave16-faces: the people gallery and the faces found on Person events
-- (faces.person_id '' = unassigned; people.alert is a WatchAlert).
CREATE TABLE IF NOT EXISTS people (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	notes      TEXT NOT NULL DEFAULT '',
	alert      TEXT NOT NULL DEFAULT 'default',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS faces (
	id         TEXT PRIMARY KEY,
	event_id   TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
	camera_id  TEXT NOT NULL,
	at_ms      INTEGER NOT NULL,
	box        TEXT NOT NULL DEFAULT '',
	score      REAL NOT NULL DEFAULT 0,
	model      TEXT NOT NULL,
	vec        BLOB NOT NULL,
	person_id  TEXT NOT NULL DEFAULT '',
	similarity REAL NOT NULL DEFAULT 0,
	crop       BLOB,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS faces_event ON faces(event_id);
CREATE INDEX IF NOT EXISTS faces_person ON faces(person_id, at_ms);
-- WAVE23: video uploads in progress (uploads.go); the parts are files in
-- <data>/uploads/<id>/ until complete moves the video into an evidence vault.
CREATE TABLE IF NOT EXISTS evidence_uploads (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL,
	user_name    TEXT NOT NULL,
	file_name    TEXT NOT NULL,
	size         INTEGER NOT NULL,
	sha256       TEXT NOT NULL,
	content_type TEXT NOT NULL,
	title        TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT '',
	recorded_at  INTEGER NOT NULL DEFAULT 0,
	camera_id    TEXT NOT NULL DEFAULT '',
	event_id     TEXT NOT NULL DEFAULT '',
	alarm_id     TEXT NOT NULL DEFAULT '',
	incident_id  TEXT NOT NULL DEFAULT '',
	device       TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL
);
`

// Options tune Open.
type Options struct {
	// Version is the running app version; it names the backup taken before
	// a schema upgrade.
	Version string
}

// Open creates the data directory (0700), opens the database and loads (or
// creates) the install key used to encrypt camera passwords.
func Open(dataDir string) (*Store, error) { return OpenWith(dataDir, Options{}) }

// OpenWith is Open with options. An existing database that is behind
// SchemaVersion is backed up (sentinel.db.bak-<version>-<time>) before its
// schema changes.
func OpenWith(dataDir string, opt Options) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "sentinel.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; avoids SQLITE_BUSY under load
	current, err := prepareMigration(db, dbPath, opt.Version)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := applyMigrations(db, current); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db); err != nil { // again: columns on tables a migration creates
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateAudioThreshold(db); err != nil { // WAVE9: lazy, idempotent (audio.go)
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateRecordingMode(db); err != nil { // WAVE22: idempotent (recording.go)
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := migrateRollNull(db); err != nil { // WAVE24 D1: once (recording.go)
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	key, err := loadOrCreateKey(filepath.Join(dataDir, "install.key"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	st := &Store{DB: db, dataDir: dataDir, aead: aead}
	if err := st.SeedRoles(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("seed roles: %w", err)
	}
	return st, nil
}

// columnAdds are additive migrations for databases created by older builds.
var columnAdds = []struct{ table, column, def string }{
	{"cameras", "fps", "INTEGER NOT NULL DEFAULT 0"},              // frame rate the camera reported over ONVIF
	{"cameras", "motion_level", "INTEGER NOT NULL DEFAULT 2"},     // 0 off, 1 low, 2 medium, 3 high
	{"cameras", "alert_on", "INTEGER NOT NULL DEFAULT 0"},         // 0 auto, 1 any motion, 2 people & vehicles, 3 people only
	{"cameras", "onvif_url", "TEXT NOT NULL DEFAULT ''"},          // ONVIF device service, for camera analytics events
	{"cameras", "camera_analytics", "INTEGER NOT NULL DEFAULT 1"}, // subscribe to the camera's own analytics (Profile M)
	{"events", "label", "TEXT NOT NULL DEFAULT ''"},
	{"events", "box", "TEXT NOT NULL DEFAULT ''"},
	{"events", "description", "TEXT NOT NULL DEFAULT ''"},
	{"events", "threat", "TEXT NOT NULL DEFAULT ''"},
	{"events", "anomaly", "INTEGER NOT NULL DEFAULT 0"},
	{"events", "reason", "TEXT NOT NULL DEFAULT ''"},
	{"events", "tags", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "note", "TEXT NOT NULL DEFAULT ''"},      // operator note / batch label on an export
	{"evidence", "from_ms", "INTEGER NOT NULL DEFAULT 0"}, // exported range (Unix ms); 0 = unknown (older locks)
	{"evidence", "to_ms", "INTEGER NOT NULL DEFAULT 0"},
	{"camera_zones", "meta", "TEXT NOT NULL DEFAULT '{}'"},           // WAVE8: line direction etc. for kind='line'
	{"cameras", "audio_detect", "INTEGER NOT NULL DEFAULT 0"},        // WAVE8: audio event detection switch
	{"cameras", "audio_threshold_db", "INTEGER NOT NULL DEFAULT 0"},  // WAVE9: proper home for the audio threshold (0 = unset)
	{"paired_devices", "activity_token", "TEXT NOT NULL DEFAULT ''"}, // WAVE10: ActivityKit push token
	// WAVE16 (lead): columns the detection-depth streams share.
	{"events", "track_id", "TEXT NOT NULL DEFAULT ''"},                  // wave16-tracking
	{"events", "person_id", "TEXT NOT NULL DEFAULT ''"},                 // wave16-faces
	{"events", "person_name", "TEXT NOT NULL DEFAULT ''"},               // wave16-faces
	{"events", "plate", "TEXT NOT NULL DEFAULT ''"},                     // wave16-plates
	{"cameras", "loiter_seconds", "INTEGER NOT NULL DEFAULT 0"},         // wave16-tracking: 0 = loitering off
	{"recording_servers", "recordings_dir", "TEXT NOT NULL DEFAULT ''"}, // where that server keeps recordings; '' = <data>/recordings
	// WAVE22 recording modes (recording.go); '' mode = not migrated yet.
	{"cameras", "recording_mode", "TEXT NOT NULL DEFAULT ''"},
	{"cameras", "record_trigger", "TEXT NOT NULL DEFAULT 'default'"},
	{"cameras", "pre_roll_seconds", "INTEGER NOT NULL DEFAULT 0"},  // pre-WAVE24; superseded by pre_roll_s
	{"cameras", "post_roll_seconds", "INTEGER NOT NULL DEFAULT 0"}, // pre-WAVE24; superseded by post_roll_s
	// WAVE24 D1: NULL = server default, a number (0 too) is literal.
	// migrateRollNull copies the old columns once (0 meant "default").
	{"cameras", "pre_roll_s", "INTEGER"},
	{"cameras", "post_roll_s", "INTEGER"},
	{"cameras", "low_res_retention_days", "INTEGER NOT NULL DEFAULT 0"},
	// WAVE23 phone cameras (phone.go) and video uploads (uploads.go).
	{"cameras", "source", "TEXT NOT NULL DEFAULT 'rtsp'"},    // rtsp | phone
	{"cameras", "push_key_hash", "TEXT NOT NULL DEFAULT ''"}, // SHA-256 of a phone camera's push key; '' = none
	{"evidence", "source", "TEXT NOT NULL DEFAULT ''"},       // '' = cut from recordings, 'upload' = uploaded video
	{"evidence", "uploaded_by", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "device", "TEXT NOT NULL DEFAULT ''"},
	{"evidence", "recorded_at", "INTEGER NOT NULL DEFAULT 0"}, // Unix ms the uploader says it was filmed; 0 = unknown
	{"evidence", "event_id", "TEXT NOT NULL DEFAULT ''"},      // the event an upload was attached to
}

// addColumns adds the columns in columnAdds that are missing. It runs
// before the migrations (they may rely on these columns) and after them
// (for tables a migration creates), skipping tables that don't exist yet.
func addColumns(db *sql.DB) error {
	for _, c := range columnAdds {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, c.table).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.column + ` ` + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) DataDir() string { return s.dataDir }

func loadOrCreateKey(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		key, err := base64.StdEncoding.DecodeString(string(data))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("install key %s is corrupt", path)
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// Seal encrypts a secret for storage; Open reverses it. Empty stays empty.
func (s *Store) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (s *Store) Unseal(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(data) < s.aead.NonceSize() {
		return "", errors.New("sealed secret is corrupt")
	}
	plain, err := s.aead.Open(nil, data[:s.aead.NonceSize()], data[s.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("sealed secret failed to decrypt")
	}
	return string(plain), nil
}

func (s *Store) Setting(ctx context.Context, key, fallback string) string {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func now() int64 { return time.Now().Unix() }
