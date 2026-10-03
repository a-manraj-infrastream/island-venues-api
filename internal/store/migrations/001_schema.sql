-- Schema for island-venues-api. Every statement is idempotent so the service
-- can run all migrations on every start-up.
CREATE TABLE IF NOT EXISTS venues (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    town             TEXT NOT NULL,
    capacity         INTEGER NOT NULL CHECK (capacity > 0),
    price_per_day_mur BIGINT NOT NULL CHECK (price_per_day_mur >= 0),
    description      TEXT NOT NULL DEFAULT '',
    tags             TEXT[] NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS bookings (
    id            TEXT PRIMARY KEY,
    venue_id      TEXT NOT NULL REFERENCES venues (id),
    venue_name    TEXT NOT NULL,
    booking_date  DATE NOT NULL,
    guests        INTEGER NOT NULL CHECK (guests > 0),
    owner         TEXT NOT NULL,
    contact_email TEXT NOT NULL,
    status        TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS bookings_owner_idx ON bookings (owner);

-- The double-booking rule lives in the database, not only in the handler:
-- two concurrent requests for the same venue and date cannot both insert a
-- live booking. CANCELLED and REJECTED bookings release the date.
CREATE UNIQUE INDEX IF NOT EXISTS bookings_live_venue_date_uidx
    ON bookings (venue_id, booking_date)
    WHERE status NOT IN ('CANCELLED', 'REJECTED');
