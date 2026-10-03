// Package events publishes domain events. booking-created is consumed by
// island-venues-notifier through an Eventarc Pub/Sub trigger.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/api/option"

	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

// publishTimeout bounds how long a request waits for Pub/Sub to acknowledge.
const publishTimeout = 10 * time.Second

// BookingCreated is the booking-created payload. Field names are part of the
// contract with the notifier: change them in both services together.
type BookingCreated struct {
	BookingID    string    `json:"bookingId"`
	VenueID      string    `json:"venueId"`
	VenueName    string    `json:"venueName"`
	Date         string    `json:"date"`
	Guests       int       `json:"guests"`
	Owner        string    `json:"owner"`
	ContactEmail string    `json:"contactEmail"`
	CreatedAt    time.Time `json:"createdAt"`
}

// NewBookingCreated builds the event for a stored booking.
func NewBookingCreated(b store.Booking) BookingCreated {
	return BookingCreated{
		BookingID:    b.ID,
		VenueID:      b.VenueID,
		VenueName:    b.VenueName,
		Date:         b.Date,
		Guests:       b.Guests,
		Owner:        b.Owner,
		ContactEmail: b.ContactEmail,
		CreatedAt:    b.CreatedAt,
	}
}

// Publisher sends booking-created events.
type Publisher interface {
	PublishBookingCreated(ctx context.Context, e BookingCreated) error
	Close() error
}

// LogPublisher is the no-op publisher used when Pub/Sub is not configured:
// it logs the event so local runs still show what would have been sent.
type LogPublisher struct {
	Logger *slog.Logger
}

// PublishBookingCreated logs the event.
func (p LogPublisher) PublishBookingCreated(ctx context.Context, e BookingCreated) error {
	logger := p.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "booking-created event (publisher disabled, not sent)",
		slog.String("bookingId", e.BookingID),
		slog.String("venueId", e.VenueID),
		slog.String("date", e.Date),
	)
	return nil
}

// Close is a no-op.
func (LogPublisher) Close() error { return nil }

// PubSubPublisher publishes to a Pub/Sub topic.
type PubSubPublisher struct {
	client    *pubsub.Client
	publisher *pubsub.Publisher
}

// NewPubSubPublisher connects to Pub/Sub using Application Default
// Credentials (the Cloud Run service account in production). opts lets tests
// point the client at a fake server.
func NewPubSubPublisher(ctx context.Context, projectID, topic string, opts ...option.ClientOption) (*PubSubPublisher, error) {
	client, err := pubsub.NewClientWithConfig(ctx, projectID, &pubsub.ClientConfig{
		// Emits publish spans as children of the request span.
		EnableOpenTelemetryTracing: true,
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create pubsub client for project %q: %w", projectID, err)
	}
	return &PubSubPublisher{client: client, publisher: client.Publisher(topic)}, nil
}

// PublishBookingCreated publishes the event and waits for the server ack.
// The W3C trace context is copied into message attributes so the notifier
// can continue the same trace.
func (p *PubSubPublisher) PublishBookingCreated(ctx context.Context, e BookingCreated) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("failed to encode booking-created event: %w", err)
	}
	attrs := map[string]string{"eventType": "booking-created"}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(attrs))

	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if _, err := p.publisher.Publish(ctx, &pubsub.Message{Data: data, Attributes: attrs}).Get(ctx); err != nil {
		return fmt.Errorf("failed to publish booking-created for booking %q: %w", e.BookingID, err)
	}
	return nil
}

// Close flushes pending messages and releases the client.
func (p *PubSubPublisher) Close() error {
	p.publisher.Stop()
	if err := p.client.Close(); err != nil {
		return fmt.Errorf("failed to close pubsub client: %w", err)
	}
	return nil
}
