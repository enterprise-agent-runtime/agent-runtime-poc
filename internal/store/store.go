// Package store implements the Warden PoC store (WRD-09, WRD-16 §11,
// design A04): SQLite via modernc.org/sqlite (no cgo, WAL), embedded
// forward-only migrations, the append-only event ledger with one hash chain
// per session plus the "sys" chain (CF-09), signed chain.checkpoint events,
// content-addressed blobs, audit export (JSON Lines) and audit verify with
// --strict (INV-A; TestAuditStrictOrdering).
//
// All events are written by one writer under a mutex; sequence numbers are
// store-global and advance only after a successful commit (A04 §10.2).
// Export and verify live here rather than in a separate internal/audit
// package (CLAUDE.md §6; CONFLICTS C-31).
package store

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"warden.dev/warden/internal/ids"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ApplicationID marks a Warden database file: 0x57415244 "WARD" (A04 §2).
const ApplicationID = 1463898692

// DefaultCheckpointEvery is the periodic checkpoint interval per chain
// (WRD-09 §4, CF-08).
const DefaultCheckpointEvery = 1000

// SysChain is the system chain id.
const SysChain = "sys"

// Errors.
var (
	ErrUnavailable     = errors.New("store unavailable")
	ErrPayloadTooLarge = errors.New("bounded event payload exceeds 64 KiB")
	ErrNotFound        = errors.New("not found")
	ErrNewerSchema     = errors.New("database was created by a newer wardend")
)

// Signer signs checkpoints with the local Ed25519 key whose private half
// lives in the keychain (A04 §11.3). It is implemented by internal/secrets
// and injected by cmd/wardend; the store never sees the private key.
type Signer interface {
	KeyID() string
	PublicKey() ed25519.PublicKey
	Sign(ctx context.Context, msg []byte) ([]byte, error)
}

// Redactor is the final redaction pass over payload strings before
// persistence (core §13.16). It returns the redacted text and the type of
// each replacement made.
type Redactor interface {
	Redact(s string) (string, []string)
}

// Options configure Open.
type Options struct {
	Dir             string // database directory (~/.warden/db)
	BlobsDir        string // ~/.warden/blobs
	RuntimeVersion  string
	User            string // "local:<os-user>"
	Clock           func() time.Time
	IDs             *ids.Generator
	Signer          Signer
	Redactor        Redactor
	PublicKeys      map[string]ed25519.PublicKey // archived checkpoint keys by key_id
	CheckpointEvery int
}

// Store is the event store.
type Store struct {
	w, r     *sql.DB
	opts     Options
	mu       sync.Mutex // the single writer
	nextSeq  int64
	types    map[string]eventType
	keys     map[string]ed25519.PublicKey
	storeID  string
	schemaV  int
	dbPath   string
	blobsDir string
}

type eventType struct {
	chains    string // S | Y | SY
	spillable bool
}

// Open opens or creates the store, applies pending migrations and checks
// consistency (A04 §2, §5).
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.Dir == "" {
		return nil, errors.New("store: Dir is required")
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.IDs == nil {
		o.IDs = ids.New()
	}
	if o.CheckpointEvery <= 0 {
		o.CheckpointEvery = DefaultCheckpointEvery
	}
	if o.User == "" {
		o.User = "local:unknown"
	}
	if o.RuntimeVersion == "" {
		o.RuntimeVersion = "dev"
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if o.BlobsDir == "" {
		o.BlobsDir = filepath.Join(filepath.Dir(o.Dir), "blobs")
	}
	path := filepath.Join(o.Dir, "warden.sqlite")
	s := &Store{opts: o, dbPath: path, blobsDir: o.BlobsDir, keys: map[string]ed25519.PublicKey{}}
	for k, v := range o.PublicKeys {
		s.keys[k] = v
	}
	if o.Signer != nil {
		s.keys[o.Signer.KeyID()] = o.Signer.PublicKey()
	}
	var err error
	s.w, err = sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.w.SetMaxOpenConns(1)
	if err := s.migrate(ctx); err != nil {
		s.w.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	s.r, err = sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		s.w.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.r.SetMaxOpenConns(4)
	if err := s.loadState(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	if s.w != nil {
		errs = append(errs, s.w.Close())
	}
	return errors.Join(errs...)
}

// StoreID returns the store's identifier (store_meta.store_id).
func (s *Store) StoreID() string { return s.storeID }

// SchemaVersion returns the highest applied migration.
func (s *Store) SchemaVersion() int { return s.schemaV }

type migration struct {
	version  int
	name     string
	sql      string
	checksum string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		num, name, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("store: bad migration name %s", e.Name())
		}
		b, _ := migrationsFS.ReadFile("migrations/" + e.Name())
		sum := sha256.Sum256(b)
		out = append(out, migration{v, name, string(b), "sha256:" + hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate bootstraps an empty file or applies pending migrations.
func (s *Store) migrate(ctx context.Context) error {
	fail := func(err error) error { return fmt.Errorf("%w: %v", ErrUnavailable, err) }
	var version string
	if err := s.w.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return fail(err)
	}
	if versionLess(version, "3.45.0") {
		return fail(fmt.Errorf("sqlite %s is older than 3.45.0", version))
	}
	ms, err := migrations()
	if err != nil {
		return fail(err)
	}
	var appID, userVersion int
	_ = s.w.QueryRowContext(ctx, "PRAGMA application_id").Scan(&appID)
	_ = s.w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion)
	if userVersion == 0 {
		var tables int
		_ = s.w.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&tables)
		if tables > 0 || appID != 0 {
			return fail(errors.New("database file is not an empty Warden store"))
		}
		for _, p := range []string{
			"PRAGMA auto_vacuum = INCREMENTAL", // before the first table
			"PRAGMA journal_mode = WAL",
			fmt.Sprintf("PRAGMA application_id = %d", ApplicationID),
			"PRAGMA wal_autocheckpoint = 1000",
			"PRAGMA journal_size_limit = 67108864",
		} {
			if _, err := s.w.ExecContext(ctx, p); err != nil {
				return fail(fmt.Errorf("%s: %w", p, err))
			}
		}
	} else if appID != ApplicationID {
		return fail(errors.New("not a Warden database (application_id)"))
	}
	applied := map[int]string{}
	if userVersion > 0 {
		rows, err := s.w.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations")
		if err != nil {
			return fail(err)
		}
		for rows.Next() {
			var v int
			var c string
			if err := rows.Scan(&v, &c); err != nil {
				rows.Close()
				return fail(err)
			}
			applied[v] = c
		}
		rows.Close()
	}
	max := ms[len(ms)-1].version
	if userVersion > max {
		return fmt.Errorf("%w (schema v%d); install that version or restore a backup", ErrNewerSchema, userVersion)
	}
	for _, m := range ms {
		if c, ok := applied[m.version]; ok {
			if c != m.checksum {
				return fail(fmt.Errorf("migration %04d was modified after it was applied", m.version))
			}
			continue
		}
		if err := s.apply(ctx, m, userVersion == 0 && m.version == 1); err != nil {
			return fail(err)
		}
	}
	s.schemaV = max
	return nil
}

func (s *Store) apply(ctx context.Context, m migration, bootstrap bool) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %04d: %w", m.version, err)
	}
	now := ts(s.opts.Clock())
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, checksum, applied_at, runtime_version) VALUES (?, ?, ?, ?, ?)",
		m.version, m.name, m.checksum, now, s.opts.RuntimeVersion); err != nil {
		return err
	}
	if bootstrap {
		g := Genesis(SysChain)
		if _, err := tx.ExecContext(ctx, "INSERT INTO chains (chain, genesis_hash, head_seq, head_hash, event_count, status, created_at) VALUES (?, ?, 0, ?, 0, 'open', ?)",
			SysChain, g, g, now); err != nil {
			return err
		}
		for k, v := range map[string]string{"store_id": s.opts.IDs.ULID(), "created_at": now, "created_by_version": s.opts.RuntimeVersion} {
			if _, err := tx.ExecContext(ctx, "INSERT INTO store_meta (key, value) VALUES (?, ?)", k, v); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return err
	}
	return tx.Commit()
}

// loadState reads the event type registry, the next sequence number and
// checks that every chain head matches its last event (A04 §2).
func (s *Store) loadState(ctx context.Context) error {
	fail := func(err error) error { return fmt.Errorf("%w: %v", ErrUnavailable, err) }
	var qc string
	if err := s.w.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&qc); err != nil || qc != "ok" {
		return fail(fmt.Errorf("quick_check: %s %v", qc, err))
	}
	if err := s.w.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key = 'store_id'").Scan(&s.storeID); err != nil {
		return fail(err)
	}
	s.types = map[string]eventType{}
	rows, err := s.w.QueryContext(ctx, "SELECT type, chains, spillable FROM event_types")
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var t, c string
		var sp int
		if err := rows.Scan(&t, &c, &sp); err != nil {
			rows.Close()
			return fail(err)
		}
		s.types[t] = eventType{c, sp == 1}
	}
	rows.Close()
	var max sql.NullInt64
	if err := s.w.QueryRowContext(ctx, "SELECT max(seq) FROM events").Scan(&max); err != nil {
		return fail(err)
	}
	s.nextSeq = max.Int64 + 1
	var bad int
	if err := s.w.QueryRowContext(ctx, `SELECT count(*) FROM chains c WHERE c.event_count > 0 AND c.head_hash <>
		(SELECT e.hash FROM events e WHERE e.chain = c.chain ORDER BY e.seq DESC LIMIT 1)`).Scan(&bad); err != nil {
		return fail(err)
	}
	if bad > 0 {
		return fail(fmt.Errorf("%d chain heads do not match their last event", bad))
	}
	return nil
}

// Genesis is the prev_hash of a chain's first event (A04 §10.1).
func Genesis(chain string) string { return sha("warden-chain-v1:" + chain) }

func sha(s string) string { return shaBytes([]byte(s)) }

func shaBytes(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// ts formats a timestamp as RFC 3339 UTC with milliseconds.
func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}
