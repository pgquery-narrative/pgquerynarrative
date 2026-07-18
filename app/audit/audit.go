// Package audit writes security and API access events to app.audit_logs.
// Used by the HTTP server to record API_REQUEST, AUTH_FAILURE, AUTH_SUCCESS, RATE_LIMIT_EXCEEDED,
// and (see Mode) higher-risk business events such as RUN_QUERY and GENERATE_REPORT.
package audit

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
)

// Event types for audit_logs.event_type.
const (
	EventAPIRequest        = "API_REQUEST"
	EventAuthFailure       = "AUTH_FAILURE"
	EventAuthSuccess       = "AUTH_SUCCESS"
	EventRateLimitExceeded = "RATE_LIMIT_EXCEEDED"
	EventUnauthorized      = "UNAUTHORIZED_ACCESS"
	EventRunQuery          = "RUN_QUERY"
	EventGenerateReport    = "GENERATE_REPORT"
	EventExportReport      = "EXPORT_REPORT"
	EventSaveQuery         = "SAVE_QUERY"
	EventDeleteQuery       = "DELETE_QUERY"
	EventInvalidSQL        = "INVALID_SQL_ATTEMPT"
	EventViewRawSQL        = "VIEW_RAW_SQL"
	EventCreateShare       = "CREATE_SHARE"
	EventRevokeShare       = "REVOKE_SHARE"
	EventManagedKeyCreate  = "MANAGED_KEY_CREATE" // #nosec G101 -- event type label, not a credential
	EventManagedKeyRevoke  = "MANAGED_KEY_REVOKE" // #nosec G101 -- event type label, not a credential
	EventMembershipChange  = "MEMBERSHIP_CHANGE"
	EventConnectionAuthz   = "CONNECTION_AUTHZ_CHANGE"
)

// Mode controls audit write durability/enforcement semantics.
type Mode string

const (
	// ModeBestEffort attempts to write the entry but never blocks or fails the caller when
	// the write fails; a metric is incremented instead. This preserves the historical
	// behavior and is the default.
	ModeBestEffort Mode = "best_effort"
	// ModeRequired writes the entry synchronously. When the write fails, Record returns the
	// error only for entries marked HighRisk, so callers protecting sensitive operations
	// (e.g. RUN_QUERY, GENERATE_REPORT) can fail closed rather than proceed unaudited.
	// Non-high-risk entries behave like ModeBestEffort even in this mode.
	ModeRequired Mode = "required"
	// ModeBuffered enqueues the entry for asynchronous delivery by a background worker and
	// returns immediately (never blocks the caller). Entries that cannot be written
	// immediately (queue full, or the write itself fails) are durably persisted to
	// app.audit_log_buffer and replayed by the worker (see ReplayBuffered), so entries are
	// not silently dropped even across process restarts.
	ModeBuffered Mode = "buffered"
)

// ParseMode normalizes a configured mode string, defaulting to ModeBestEffort for empty or
// unrecognized values.
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeRequired:
		return ModeRequired
	case ModeBuffered:
		return ModeBuffered
	default:
		return ModeBestEffort
	}
}

// ValidMode reports whether s is a recognized mode (or empty, which defaults to
// ModeBestEffort). Used by configuration validation to fail fast on typos.
func ValidMode(s string) bool {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeBestEffort, ModeRequired, ModeBuffered, "":
		return true
	default:
		return false
	}
}

// Entry represents a single audit log row.
type Entry struct {
	EventType  string
	EntityType string
	EntityID   *string
	Details    map[string]interface{}
	UserID     string
	IP         string
	UserAgent  string
	OrgID      string
	// HighRisk marks entries for sensitive operations (e.g. RUN_QUERY, GENERATE_REPORT).
	// In ModeRequired, a write failure for a HighRisk entry is returned to the caller so the
	// triggering operation can be denied (fail closed) instead of proceeding unaudited.
	HighRisk bool
}

// bufferQueueSize bounds the in-memory queue for ModeBuffered before entries spill over to
// the durable buffer table synchronously.
const bufferQueueSize = 1000

// replayBatchSize is the number of buffered rows the background worker replays per tick.
const replayBatchSize = 50

// Store writes audit entries to the database.
type Store struct {
	pool *pgxpool.Pool
	mode Mode

	queue    chan Entry
	stopCh   chan struct{}
	stopOnce sync.Once
	workerWG sync.WaitGroup
}

// NewStore returns an audit store that writes to the given app pool using mode. When mode is
// ModeBuffered, a background worker is started to drain the queue and replay the durable
// buffer table; call Close to stop it during graceful shutdown.
func NewStore(pool *pgxpool.Pool, mode Mode) *Store {
	s := &Store{pool: pool, mode: ParseMode(string(mode))}
	if s.mode == ModeBuffered && pool != nil {
		s.queue = make(chan Entry, bufferQueueSize)
		s.stopCh = make(chan struct{})
		s.workerWG.Add(1)
		go s.bufferedWorker()
	}
	return s
}

// Mode returns the store's configured durability mode.
func (s *Store) Mode() Mode {
	if s == nil {
		return ModeBestEffort
	}
	return s.mode
}

// Close stops the background buffered worker (if running) and waits for it to drain its
// current queue. Safe to call on a nil Store or a store not in ModeBuffered.
func (s *Store) Close() {
	if s == nil || s.stopCh == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.workerWG.Wait()
}

// Record writes one audit entry according to the store's configured Mode:
//   - ModeBestEffort: attempts the write; failures are metriced, never returned.
//   - ModeRequired: writes synchronously; failures are returned only for HighRisk entries.
//   - ModeBuffered: enqueues for async durable delivery; never blocks or fails the caller.
//
// Record itself never panics on a nil Store or nil pool (no-op).
func (s *Store) Record(ctx context.Context, e Entry) error {
	if s == nil || s.pool == nil {
		return nil
	}
	switch s.mode {
	case ModeBuffered:
		s.enqueueBuffered(e)
		return nil
	case ModeRequired:
		if err := s.writeEntry(ctx, e); err != nil {
			observability.IncAuditWriteFailure()
			if e.HighRisk {
				return err
			}
		}
		return nil
	default:
		if err := s.writeEntry(ctx, e); err != nil {
			observability.IncAuditWriteFailure()
		}
		return nil
	}
}

// enqueueBuffered attempts a non-blocking enqueue; when the in-memory queue is full it spills
// directly to the durable buffer table so the entry is not dropped.
func (s *Store) enqueueBuffered(e Entry) {
	select {
	case s.queue <- e:
	default:
		bctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.persistToBuffer(bctx, e, "queue full"); err != nil {
			observability.IncAuditWriteFailure()
		}
	}
}

func (s *Store) bufferedWorker() {
	defer s.workerWG.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			s.drainQueue()
			return
		case e := <-s.queue:
			s.deliverOrBuffer(e)
		case <-ticker.C:
			_, _, _ = s.ReplayBuffered(context.Background(), replayBatchSize)
		}
	}
}

// drainQueue flushes any entries still queued at shutdown time to the durable buffer table
// (best effort) so a graceful shutdown does not lose in-flight entries.
func (s *Store) drainQueue() {
	for {
		select {
		case e := <-s.queue:
			s.deliverOrBuffer(e)
		default:
			return
		}
	}
}

func (s *Store) deliverOrBuffer(e Entry) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.writeEntry(ctx, e); err != nil {
		if bufErr := s.persistToBuffer(ctx, e, err.Error()); bufErr != nil {
			observability.IncAuditWriteFailure()
		}
	}
}

// ReplayBuffered attempts to deliver up to limit rows from the durable buffer table, deleting
// each on success and leaving it (with an incremented attempt count) for the next replay on
// failure. Uses FOR UPDATE SKIP LOCKED so multiple replicas can replay concurrently without
// double-delivering. Returns the number of rows replayed successfully and the number that
// remain (best-effort estimate; remaining is only counted for rows this call attempted).
func (s *Store) ReplayBuffered(ctx context.Context, limit int) (replayed, remaining int, err error) {
	if s == nil || s.pool == nil {
		return 0, 0, nil
	}
	if limit <= 0 {
		limit = replayBatchSize
	}
	// FOR UPDATE SKIP LOCKED must hold its lock across both the SELECT and the follow-up
	// DELETE/UPDATE so concurrent replicas replaying at the same time never double-deliver
	// the same buffered entry; that requires an explicit transaction (an implicit,
	// non-transactional statement would release the lock as soon as the SELECT completes).
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	rows, err := tx.Query(ctx, `
		SELECT id, event_type, entity_type, entity_id, details, user_id, ip_address, user_agent, organization_id
		FROM app.audit_log_buffer
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return 0, 0, err
	}
	type bufferedRow struct {
		id     string
		entry  Entry
		detail []byte
	}
	var pending []bufferedRow
	for rows.Next() {
		var br bufferedRow
		var entityID *string
		var ip *net.IP
		var userID, userAgent, orgID *string
		if scanErr := rows.Scan(&br.id, &br.entry.EventType, &br.entry.EntityType, &entityID, &br.detail, &userID, &ip, &userAgent, &orgID); scanErr != nil {
			rows.Close()
			return 0, 0, scanErr
		}
		br.entry.EntityID = entityID
		if userID != nil {
			br.entry.UserID = *userID
		}
		if userAgent != nil {
			br.entry.UserAgent = *userAgent
		}
		if orgID != nil {
			br.entry.OrgID = *orgID
		}
		if ip != nil {
			br.entry.IP = ip.String()
		}
		if len(br.detail) > 0 {
			var details map[string]interface{}
			if jsonErr := json.Unmarshal(br.detail, &details); jsonErr == nil {
				br.entry.Details = details
			}
		}
		pending = append(pending, br)
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return 0, 0, rowsErr
	}
	for _, br := range pending {
		// writeEntry uses its own connection (audit_logs is unrelated to the buffer-row
		// lock held by this transaction), so it is safe to call while tx is still open.
		if writeErr := s.writeEntry(ctx, br.entry); writeErr != nil {
			remaining++
			if _, updErr := tx.Exec(ctx, `UPDATE app.audit_log_buffer SET attempts = attempts + 1, last_error = $2 WHERE id = $1::uuid`, br.id, writeErr.Error()); updErr != nil {
				return replayed, remaining, updErr
			}
			continue
		}
		if _, delErr := tx.Exec(ctx, `DELETE FROM app.audit_log_buffer WHERE id = $1::uuid`, br.id); delErr != nil {
			return replayed, remaining, delErr
		}
		replayed++
	}
	if err := tx.Commit(ctx); err != nil {
		return replayed, remaining, err
	}
	committed = true
	return replayed, remaining, nil
}

func (s *Store) persistToBuffer(ctx context.Context, e Entry, lastError string) error {
	detailsJSON, _ := json.Marshal(e.Details)
	var ip net.IP
	if e.IP != "" {
		ip = net.ParseIP(e.IP)
	}
	orgID := resolveOrgID(ctx, e.OrgID)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app.audit_log_buffer (event_type, entity_type, entity_id, details, user_id, ip_address, user_agent, organization_id, last_error)
		 VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, NULLIF($7,''), $8::uuid, $9)`,
		e.EventType, e.EntityType, e.EntityID, detailsJSON, e.UserID, ip, e.UserAgent, orgID, lastError,
	)
	return err
}

func resolveOrgID(ctx context.Context, explicit string) string {
	orgID := strings.TrimSpace(explicit)
	if orgID == "" {
		orgID = auth.OrgIDFromContext(ctx)
	}
	if orgID == "" {
		orgID = auth.DefaultOrgID()
	}
	return orgID
}

// writeEntry performs the actual insert into app.audit_logs, scoping the connection's
// session to the entry's organization for RLS.
func (s *Store) writeEntry(ctx context.Context, e Entry) error {
	if s == nil || s.pool == nil {
		return nil
	}
	detailsJSON, _ := json.Marshal(e.Details)
	var ip net.IP
	if e.IP != "" {
		ip = net.ParseIP(e.IP)
	}
	orgID := resolveOrgID(ctx, e.OrgID)
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, orgID); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT set_config('app.current_org_id', '', false)`)
	}()
	_, err = conn.Exec(ctx,
		`INSERT INTO app.audit_logs (event_type, entity_type, entity_id, details, user_id, ip_address, user_agent, organization_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, NULLIF($7,''), $8::uuid)`,
		e.EventType, e.EntityType, e.EntityID, detailsJSON, e.UserID, ip, e.UserAgent, orgID,
	)
	return err
}
