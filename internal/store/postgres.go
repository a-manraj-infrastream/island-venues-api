package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockID is an arbitrary constant used as a PostgreSQL advisory lock
// key. Several Cloud Run instances can start at once; the lock serialises
// their migration runs so concurrent CREATE ... IF NOT EXISTS statements do
// not race on the system catalogue.
const migrationLockID = 73112026

// PostgreSQL error codes mapped onto store errors.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// bookingColumns is shared by every query that returns a Booking so the
// column order always matches scanBooking.
const bookingColumns = `id, venue_id, venue_name, to_char(booking_date, 'YYYY-MM-DD'), guests, owner, contact_email, status, created_at`

// Postgres is a Store backed by PostgreSQL (AlloyDB in production).
type Postgres struct {
	pool *pgxpool.Pool
}

var _ Store = (*Postgres)(nil)

// OpenPostgres connects to databaseURL, verifies the connection and runs the
// embedded migrations. The caller owns the returned store and must Close it.
func OpenPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	p := &Postgres{pool: pool}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

// migrate applies every embedded migration, in file-name order, inside one
// transaction guarded by an advisory lock. Each file is idempotent, so running
// them on every start-up is safe.
func (p *Postgres) migrate(ctx context.Context) error {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("failed to list migrations: %w", err)
	}
	sort.Strings(names)

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("failed to take migration lock: %w", err)
	}
	for _, name := range names {
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", name, err)
		}
		// No arguments: pgx uses the simple protocol, which accepts the
		// multi-statement files.
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("failed to apply migration %s: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit migrations: %w", err)
	}
	return nil
}

// ListVenues returns all venues ordered by town then name. COLLATE "C" makes
// the order byte-wise, identical to the in-memory store.
func (p *Postgres) ListVenues(ctx context.Context) ([]Venue, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, name, town, capacity, price_per_day_mur, description, tags
		FROM venues ORDER BY town COLLATE "C", name COLLATE "C"`)
	if err != nil {
		return nil, fmt.Errorf("failed to query venues: %w", err)
	}
	venues, err := pgx.CollectRows(rows, scanVenue)
	if err != nil {
		return nil, fmt.Errorf("failed to read venues: %w", err)
	}
	return venues, nil
}

// GetVenue returns one venue or ErrNotFound.
func (p *Postgres) GetVenue(ctx context.Context, id string) (Venue, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, name, town, capacity, price_per_day_mur, description, tags
		FROM venues WHERE id = $1`, id)
	if err != nil {
		return Venue{}, fmt.Errorf("failed to query venue %q: %w", id, err)
	}
	v, err := pgx.CollectExactlyOneRow(rows, scanVenue)
	if err != nil {
		return Venue{}, fmt.Errorf("failed to get venue %q: %w", id, mapError(err))
	}
	return v, nil
}

// CreateVenue inserts a venue or returns ErrConflict if the id exists.
func (p *Postgres) CreateVenue(ctx context.Context, v Venue) error {
	tags := v.Tags
	if tags == nil {
		tags = []string{}
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO venues (id, name, town, capacity, price_per_day_mur, description, tags)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, v.ID, v.Name, v.Town, v.Capacity, v.PricePerDayMur, v.Description, tags)
	if err != nil {
		return fmt.Errorf("failed to create venue %q: %w", v.ID, mapError(err))
	}
	return nil
}

// CreateBooking inserts a booking. The partial unique index on
// (venue_id, booking_date) enforces the double-booking rule atomically.
func (p *Postgres) CreateBooking(ctx context.Context, b Booking) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO bookings (id, venue_id, venue_name, booking_date, guests, owner, contact_email, status, created_at)
		VALUES ($1, $2, $3, $4::date, $5, $6, $7, $8, $9)`,
		b.ID, b.VenueID, b.VenueName, b.Date, b.Guests, b.Owner, b.ContactEmail, b.Status, b.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to create booking for venue %q on %s: %w", b.VenueID, b.Date, mapError(err))
	}
	return nil
}

// GetBooking returns one booking or ErrNotFound.
func (p *Postgres) GetBooking(ctx context.Context, id string) (Booking, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+bookingColumns+` FROM bookings WHERE id = $1`, id)
	if err != nil {
		return Booking{}, fmt.Errorf("failed to query booking %q: %w", id, err)
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBooking)
	if err != nil {
		return Booking{}, fmt.Errorf("failed to get booking %q: %w", id, mapError(err))
	}
	return b, nil
}

// ListBookings returns all bookings, or one owner's, newest first.
func (p *Postgres) ListBookings(ctx context.Context, owner string) ([]Booking, error) {
	query := `SELECT ` + bookingColumns + ` FROM bookings`
	args := []any{}
	if owner != "" {
		query += ` WHERE owner = $1`
		args = append(args, owner)
	}
	query += ` ORDER BY created_at DESC, id`
	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query bookings: %w", err)
	}
	bookings, err := pgx.CollectRows(rows, scanBooking)
	if err != nil {
		return nil, fmt.Errorf("failed to read bookings: %w", err)
	}
	return bookings, nil
}

// TransitionBooking performs a compare-and-set on the status column so two
// concurrent transitions cannot both start from the same status.
func (p *Postgres) TransitionBooking(ctx context.Context, id string, from []string, to string) (Booking, error) {
	rows, err := p.pool.Query(ctx, `UPDATE bookings SET status = $2 WHERE id = $1 AND status = ANY($3)
		RETURNING `+bookingColumns, id, to, from)
	if err != nil {
		return Booking{}, fmt.Errorf("failed to update booking %q: %w", id, err)
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBooking)
	if err == nil {
		return b, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Booking{}, fmt.Errorf("failed to move booking %q to %s: %w", id, to, mapError(err))
	}
	// No row updated: either the booking does not exist or its status does
	// not allow this transition. Read it back to tell the two apart.
	current, getErr := p.GetBooking(ctx, id)
	if getErr != nil {
		return Booking{}, fmt.Errorf("failed to transition booking %q: %w", id, getErr)
	}
	return current, fmt.Errorf("failed to move booking %q from %s to %s: %w", id, current.Status, to, ErrInvalidTransition)
}

func scanVenue(row pgx.CollectableRow) (Venue, error) {
	var v Venue
	if err := row.Scan(&v.ID, &v.Name, &v.Town, &v.Capacity, &v.PricePerDayMur, &v.Description, &v.Tags); err != nil {
		return Venue{}, fmt.Errorf("failed to scan venue: %w", err)
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v, nil
}

func scanBooking(row pgx.CollectableRow) (Booking, error) {
	var b Booking
	var created time.Time
	if err := row.Scan(&b.ID, &b.VenueID, &b.VenueName, &b.Date, &b.Guests, &b.Owner, &b.ContactEmail, &b.Status, &created); err != nil {
		return Booking{}, fmt.Errorf("failed to scan booking: %w", err)
	}
	b.CreatedAt = created.UTC()
	return b, nil
}

// mapError converts driver errors into the store's sentinel errors while
// keeping the original in the chain for logging.
func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return fmt.Errorf("%w: %w", ErrConflict, err)
		case pgForeignKeyViolation:
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		}
	}
	return err
}
