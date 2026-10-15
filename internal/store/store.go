package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	DB              *sql.DB
	cursorKey       []byte
	reviewCursorKey []byte
	clock           func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate activity cursor key: %w", err)
	}
	return openWithActivityCursorKey(ctx, path, key)
}

// OpenWithActivityCursorSecret keeps opaque activity cursors stable across
// process restarts. Production callers should derive this from their existing
// high-entropy session secret rather than introducing another configuration
// secret.
func OpenWithActivityCursorSecret(ctx context.Context, path string, secret []byte) (*Store, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("activity cursor secret must contain at least 32 bytes")
	}
	return openWithActivityCursorKey(ctx, path, secret)
}

func openWithActivityCursorKey(ctx context.Context, path string, key []byte) (*Store, error) {
	if err := rejectUnsupportedSQLiteURI(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	pragmas := []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"}
	for _, pragma := range pragmas {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	if err = Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, cursorKey: append([]byte(nil), key...), clock: func() time.Time { return time.Now().UTC() }}, nil
}

// modernc.org/sqlite cannot bound or cancel its internal table-lock retry for
// SQLITE_LOCKED_SHAREDCACHE. Shared-cache URIs are therefore not a supported
// Store configuration; keep the defensive error classification in review.go,
// but reject this mode before opening a database connection.
func rejectUnsupportedSQLiteURI(path string) error {
	if !strings.HasPrefix(strings.ToLower(path), "file:") {
		return nil
	}
	queryStart := strings.IndexByte(path, '?')
	if queryStart < 0 {
		return nil
	}
	for _, parameter := range strings.Split(path[queryStart+1:], "&") {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok {
			continue
		}
		decodedKey, keyErr := url.QueryUnescape(key)
		decodedValue, valueErr := url.QueryUnescape(value)
		if keyErr != nil || valueErr != nil {
			continue
		}
		if strings.EqualFold(decodedKey, "cache") && strings.EqualFold(decodedValue, "shared") {
			return fmt.Errorf("unsupported sqlite URI: shared-cache mode is not supported")
		}
	}
	return nil
}

// SetClock provides deterministic time to validation tests and callers that
// coordinate several writes against one logical clock.
func (s *Store) SetClock(clock func() time.Time) {
	if clock == nil {
		s.clock = func() time.Time { return time.Now().UTC() }
		return
	}
	s.clock = clock
}

func (s *Store) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock().UTC()
}

// SetActivityCursorSecret is used by the authenticated HTTP constructor. It
// intentionally copies the derived key and never persists or logs it.
func (s *Store) SetActivityCursorSecret(secret []byte) error {
	if len(secret) < 32 {
		return fmt.Errorf("activity cursor secret must contain at least 32 bytes")
	}
	s.cursorKey = append(s.cursorKey[:0], secret...)
	return nil
}

// SetReviewCursorSecret installs the process-local, purpose-specific key used
// to sign review list cursors. It is never persisted or logged.
func (s *Store) SetReviewCursorSecret(secret []byte) error {
	if len(secret) < 32 {
		return fmt.Errorf("review cursor secret must contain at least 32 bytes")
	}
	s.reviewCursorKey = append(s.reviewCursorKey[:0], secret...)
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	for _, entry := range entries {
		version := entry.Name()
		var exists int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + version)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))", version); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	if err := verifyActivitySchema(ctx, db); err != nil {
		return fmt.Errorf("canonical activity schema verification failed: %w", err)
	}
	if err := verifyThesisSchema(ctx, db); err != nil {
		return fmt.Errorf("canonical thesis schema verification failed: %w", err)
	}
	return nil
}

func verifyThesisSchema(ctx context.Context, db *sql.DB) error {
	if err := verifyThesisTableDefinition(ctx, db, "portfolio_theses", []string{
		"current_revision integer not null check (current_revision >= 1)",
		"unique(portfolio_id, holding_id)",
		"unique(id, portfolio_id, holding_id, instrument_id)",
		"foreign key (portfolio_id, holding_id) references lots(portfolio_id, id)",
	}); err != nil {
		return err
	}
	if err := verifyThesisTableDefinition(ctx, db, "thesis_revisions", []string{
		"revision integer not null check (revision >= 1)",
		"unique(thesis_id, revision)",
		"unique(thesis_id, request_hash)",
		"foreign key (thesis_id, portfolio_id, holding_id, instrument_id) references portfolio_theses(id, portfolio_id, holding_id, instrument_id)",
	}); err != nil {
		return err
	}
	if err := verifyTableColumns(ctx, db, "portfolio_theses", []string{
		"id TEXT 0 1", "portfolio_id TEXT 1 0", "holding_id TEXT 1 0", "instrument_id TEXT 1 0",
		"summary TEXT 1 0", "intended_holding_period TEXT 1 0", "allocation_limit TEXT 0 0",
		"reasons_to_hold TEXT 1 0", "risks TEXT 1 0", "reasons_to_sell TEXT 1 0", "conditions_to_change TEXT 1 0",
		"sources TEXT 1 0", "current_revision INTEGER 1 0", "request_hash TEXT 1 0", "created_at TEXT 1 0", "updated_at TEXT 1 0",
	}); err != nil {
		return err
	}
	if err := verifyTableColumns(ctx, db, "thesis_revisions", []string{
		"id TEXT 0 1", "thesis_id TEXT 1 0", "portfolio_id TEXT 1 0", "holding_id TEXT 1 0", "instrument_id TEXT 1 0",
		"revision INTEGER 1 0", "summary TEXT 1 0", "intended_holding_period TEXT 1 0", "allocation_limit TEXT 0 0",
		"reasons_to_hold TEXT 1 0", "risks TEXT 1 0", "reasons_to_sell TEXT 1 0", "conditions_to_change TEXT 1 0",
		"sources TEXT 1 0", "request_hash TEXT 1 0", "created_at TEXT 1 0",
	}); err != nil {
		return err
	}
	for _, index := range []struct {
		name   string
		cols   []string
		desc   []int
		unique bool
	}{
		{"idx_lots_portfolio_id_id", []string{"portfolio_id", "id"}, []int{0, 0}, true},
		{"idx_portfolio_theses_portfolio", []string{"portfolio_id"}, []int{0}, false},
		{"idx_portfolio_theses_instrument", []string{"portfolio_id", "instrument_id"}, []int{0, 0}, false},
		{"idx_thesis_revisions_history", []string{"thesis_id", "revision"}, []int{0, 1}, false},
		{"idx_thesis_revisions_portfolio", []string{"portfolio_id", "holding_id", "revision"}, []int{0, 0, 1}, false},
	} {
		if index.unique {
			var unique int
			table := "portfolio_theses"
			if index.name == "idx_lots_portfolio_id_id" {
				table = "lots"
			}
			if err := db.QueryRowContext(ctx, "SELECT \"unique\" FROM pragma_index_list("+quoteSQLiteIdentifier(table)+") WHERE name=?", index.name).Scan(&unique); err != nil || unique != 1 {
				return fmt.Errorf("index %s is missing or not unique", index.name)
			}
		}
		if err := verifyIndexColumns(ctx, db, index.name, index.cols, index.desc); err != nil {
			return err
		}
	}
	for _, unique := range []struct {
		table string
		cols  []string
	}{
		{"portfolio_theses", []string{"portfolio_id", "holding_id"}},
		{"portfolio_theses", []string{"id", "portfolio_id", "holding_id", "instrument_id"}},
		{"thesis_revisions", []string{"thesis_id", "revision"}},
		{"thesis_revisions", []string{"thesis_id", "request_hash"}},
	} {
		if err := verifyAnyUniqueColumns(ctx, db, unique.table, unique.cols); err != nil {
			return err
		}
	}
	if err := verifyForeignKey(ctx, db, "portfolio_theses", []string{"portfolio_id", "holding_id"}, "lots", []string{"portfolio_id", "id"}); err != nil {
		return err
	}
	if err := verifyForeignKey(ctx, db, "thesis_revisions", []string{"thesis_id", "portfolio_id", "holding_id", "instrument_id"}, "portfolio_theses", []string{"id", "portfolio_id", "holding_id", "instrument_id"}); err != nil {
		return err
	}
	for _, trigger := range []struct {
		name, operation string
		fragments       []string
	}{
		{"portfolio_theses_scope_insert", "before insert on portfolio_theses", []string{"new.holding_id", "new.portfolio_id", "new.instrument_id", "raise(abort"}},
		{"portfolio_theses_scope_update", "before update of portfolio_id, holding_id, instrument_id on portfolio_theses", []string{"new.holding_id", "new.portfolio_id", "new.instrument_id", "raise(abort"}},
		{"thesis_revisions_scope_insert", "before insert on thesis_revisions", []string{"new.holding_id", "new.portfolio_id", "new.instrument_id", "raise(abort"}},
		{"portfolio_theses_append_only_delete", "before delete on portfolio_theses", []string{"append-only", "raise(abort"}},
		{"thesis_revisions_append_only_update", "before update on thesis_revisions", []string{"append-only", "raise(abort"}},
		{"thesis_revisions_append_only_delete", "before delete on thesis_revisions", []string{"append-only", "raise(abort"}},
	} {
		var definition string
		if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", trigger.name).Scan(&definition); err != nil {
			return fmt.Errorf("thesis trigger %s: %w", trigger.name, err)
		}
		definition = strings.ToLower(definition)
		if !strings.Contains(definition, trigger.operation) {
			return fmt.Errorf("thesis trigger %s has wrong operation", trigger.name)
		}
		for _, fragment := range trigger.fragments {
			if !strings.Contains(definition, fragment) {
				return fmt.Errorf("thesis trigger %s is missing %q", trigger.name, fragment)
			}
		}
		if canonicalSQLiteSQL(definition) != canonicalSQLiteSQL(thesisTriggerDefinition(trigger.name)) {
			return fmt.Errorf("thesis trigger %s has an unexpected definition", trigger.name)
		}
	}
	return nil
}

func thesisTriggerDefinition(name string) string {
	switch name {
	case "portfolio_theses_scope_insert":
		return `CREATE TRIGGER portfolio_theses_scope_insert
BEFORE INSERT ON portfolio_theses BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis holding scope mismatch') END;
END`
	case "portfolio_theses_scope_update":
		return `CREATE TRIGGER portfolio_theses_scope_update
BEFORE UPDATE OF portfolio_id, holding_id, instrument_id ON portfolio_theses BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis holding scope mismatch') END;
END`
	case "thesis_revisions_scope_insert":
		return `CREATE TRIGGER thesis_revisions_scope_insert
BEFORE INSERT ON thesis_revisions BEGIN
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM lots WHERE id = NEW.holding_id AND portfolio_id = NEW.portfolio_id AND instrument_id = NEW.instrument_id
    ) THEN RAISE(ABORT, 'thesis revision holding scope mismatch') END;
END`
	case "portfolio_theses_append_only_delete":
		return `CREATE TRIGGER portfolio_theses_append_only_delete
BEFORE DELETE ON portfolio_theses BEGIN
    SELECT RAISE(ABORT, 'theses are append-only');
END`
	case "thesis_revisions_append_only_update":
		return `CREATE TRIGGER thesis_revisions_append_only_update
BEFORE UPDATE ON thesis_revisions BEGIN
    SELECT RAISE(ABORT, 'thesis revisions are append-only');
END`
	case "thesis_revisions_append_only_delete":
		return `CREATE TRIGGER thesis_revisions_append_only_delete
BEFORE DELETE ON thesis_revisions BEGIN
    SELECT RAISE(ABORT, 'thesis revisions are append-only');
END`
	default:
		return ""
	}
}

func verifyThesisTableDefinition(ctx context.Context, db *sql.DB, table string, fragments []string) error {
	var definition string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&definition); err != nil {
		return fmt.Errorf("thesis table %s: %w", table, err)
	}
	definition = canonicalSQLiteSQL(definition)
	for _, fragment := range fragments {
		if !strings.Contains(definition, canonicalSQLiteSQL(fragment)) {
			return fmt.Errorf("thesis table %s is missing %q", table, fragment)
		}
	}
	return nil
}

func canonicalSQLiteSQL(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func verifyTableColumns(ctx context.Context, db *sql.DB, table string, expected []string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for i, item := range expected {
		if !rows.Next() {
			return fmt.Errorf("%s column %d is missing", table, i)
		}
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return err
		}
		parts := strings.Fields(item)
		if len(parts) != 4 || cid != i || name != parts[0] || strings.ToUpper(typ) != parts[1] || fmt.Sprint(notnull) != parts[2] || fmt.Sprint(pk) != parts[3] {
			return fmt.Errorf("%s column %d is %s/%s notnull=%d pk=%d, expected %s", table, i, name, typ, notnull, pk, item)
		}
	}
	if rows.Next() {
		return fmt.Errorf("%s has unexpected columns", table)
	}
	return rows.Err()
}

func verifyAnyUniqueColumns(ctx context.Context, db *sql.DB, table string, expected []string) error {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_index_list("+quoteSQLiteIdentifier(table)+") WHERE \"unique\"=1")
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, name := range names {
		if verifyIndexColumns(ctx, db, name, expected, make([]int, len(expected))) == nil {
			return nil
		}
	}
	return fmt.Errorf("%s unique constraint %v is missing", table, expected)
}

func verifyForeignKey(ctx context.Context, db *sql.DB, table string, from []string, toTable string, to []string) error {
	rows, err := db.QueryContext(ctx, "SELECT id,seq,\"table\",\"from\",\"to\",on_update,on_delete FROM pragma_foreign_key_list("+quoteSQLiteIdentifier(table)+") ORDER BY id,seq")
	if err != nil {
		return err
	}
	defer rows.Close()
	type fkGroup struct {
		parent    string
		pairs     []string
		actionsOK bool
	}
	groups := map[int]*fkGroup{}
	for rows.Next() {
		var id, seq int
		var parent, source, target, onUpdate, onDelete string
		if err := rows.Scan(&id, &seq, &parent, &source, &target, &onUpdate, &onDelete); err != nil {
			return err
		}
		if groups[id] == nil {
			groups[id] = &fkGroup{parent: parent, actionsOK: true}
		}
		groups[id].pairs = append(groups[id].pairs, source, target)
		if onUpdate != "NO ACTION" || onDelete != "NO ACTION" {
			groups[id].actionsOK = false
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, group := range groups {
		pair := group.pairs
		if group.parent != toTable || !group.actionsOK || len(pair) != 2*len(from) {
			continue
		}
		matched := true
		for i := range from {
			if pair[2*i] != from[i] || pair[2*i+1] != to[i] {
				matched = false
				break
			}
		}
		if matched {
			return nil
		}
	}
	return fmt.Errorf("%s foreign key %v -> %s %v is missing", table, from, toTable, to)
}

func verifyActivitySchema(ctx context.Context, db *sql.DB) error {
	expectedColumns := []struct {
		name, typ   string
		notnull, pk int
	}{
		{"id", "TEXT", 0, 1}, {"portfolio_id", "TEXT", 1, 0}, {"actor_category", "TEXT", 1, 0},
		{"action", "TEXT", 1, 0}, {"category", "TEXT", 1, 0}, {"entity_type", "TEXT", 0, 0},
		{"entity_id", "TEXT", 0, 0}, {"summary", "TEXT", 1, 0}, {"details", "TEXT", 1, 0},
		{"occurred_at", "TEXT", 1, 0}, {"sort_time", "TEXT", 1, 0}, {"source_audit_event_id", "TEXT", 0, 0},
		{"retain_until", "TEXT", 1, 0},
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(activity_events)")
	if err != nil {
		return err
	}
	defer rows.Close()
	for index, expected := range expectedColumns {
		if !rows.Next() {
			return fmt.Errorf("activity_events column %q is missing", expected.name)
		}
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			return err
		}
		if cid != index || name != expected.name || strings.ToUpper(typ) != expected.typ || notnull != expected.notnull || pk != expected.pk {
			return fmt.Errorf("activity_events column %d is %s/%s notnull=%d pk=%d, expected %s/%s notnull=%d pk=%d", index, name, typ, notnull, pk, expected.name, expected.typ, expected.notnull, expected.pk)
		}
	}
	if rows.Next() {
		return errors.New("activity_events has unexpected columns")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var foreignKeyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_list('activity_events') WHERE "table"='portfolios' AND "from"='portfolio_id' AND "to"='id'`).Scan(&foreignKeyCount); err != nil {
		return err
	}
	if foreignKeyCount != 1 {
		return errors.New("activity_events portfolio foreign key is missing or incorrect")
	}
	if err := verifyForeignKey(ctx, db, "activity_events", []string{"source_audit_event_id", "portfolio_id"}, "audit_event_portfolios", []string{"audit_event_id", "portfolio_id"}); err != nil {
		return err
	}
	var unique int
	if err := db.QueryRowContext(ctx, `SELECT "unique" FROM pragma_index_list('activity_events') WHERE name='idx_activity_source_portfolio'`).Scan(&unique); err != nil {
		return fmt.Errorf("activity source index: %w", err)
	}
	if unique != 1 {
		return errors.New("activity source index is not unique")
	}
	if err := verifyIndexColumns(ctx, db, "idx_activity_source_portfolio", []string{"source_audit_event_id", "portfolio_id"}, []int{0, 0}); err != nil {
		return err
	}
	if err := verifyIndexColumns(ctx, db, "idx_activity_portfolio_order", []string{"portfolio_id", "sort_time", "id"}, []int{0, 1, 1}); err != nil {
		return err
	}
	var tableSQL string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='activity_events'").Scan(&tableSQL); err != nil {
		return fmt.Errorf("activity table definition: %w", err)
	}
	lowerSQL := strings.ToLower(tableSQL)
	for _, fragment := range []string{"length(occurred_at)=30", "glob", "strftime", "julianday", "sort_time=occurred_at", "length(retain_until)=30"} {
		if !strings.Contains(lowerSQL, fragment) {
			return fmt.Errorf("activity timestamp check is missing %q", fragment)
		}
	}
	for _, trigger := range []struct{ name, operation string }{{"activity_events_append_only_update", "before update on activity_events"}, {"activity_events_append_only_delete", "before delete on activity_events"}} {
		var triggerSQL string
		if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", trigger.name).Scan(&triggerSQL); err != nil {
			return fmt.Errorf("activity trigger %s: %w", trigger.name, err)
		}
		triggerSQL = strings.ToLower(triggerSQL)
		if !strings.Contains(triggerSQL, "append-only") || !strings.Contains(triggerSQL, trigger.operation) || !strings.Contains(triggerSQL, "raise(abort") {
			return fmt.Errorf("activity trigger %s is not append-only", trigger.name)
		}
	}
	return nil
}

func verifyIndexColumns(ctx context.Context, db *sql.DB, name string, expected []string, expectedDesc []int) error {
	rows, err := db.QueryContext(ctx, "PRAGMA index_xinfo("+quoteSQLiteIdentifier(name)+")")
	if err != nil {
		return fmt.Errorf("index %s: %w", name, err)
	}
	defer rows.Close()
	for index := range expected {
		if !rows.Next() {
			return fmt.Errorf("index %s is missing column %s", name, expected[index])
		}
		var seqno, cid, desc, key int
		var column, collation string
		if err := rows.Scan(&seqno, &cid, &column, &desc, &collation, &key); err != nil {
			return err
		}
		if seqno != index || column != expected[index] || desc != expectedDesc[index] || key != 1 {
			return fmt.Errorf("index %s column %d is %s desc=%d, expected %s desc=%d", name, index, column, desc, expected[index], expectedDesc[index])
		}
	}
	return nil
}

func quoteSQLiteIdentifier(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
