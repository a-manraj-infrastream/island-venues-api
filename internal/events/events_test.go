package events

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/a-manraj-infrastream/island-venues-api/internal/store"
)

func sampleEvent() BookingCreated {
	return NewBookingCreated(store.Booking{
		ID: "bk-1", VenueID: "grand-baie-lagoon-deck", VenueName: "Lagoon Deck", Date: "2026-10-04",
		Guests: 10, Owner: "alice@example.mu", ContactEmail: "contact@example.mu", Status: store.StatusPending,
		CreatedAt: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC),
	})
}

func TestLogPublisher(t *testing.T) {
	var buf bytes.Buffer
	p := LogPublisher{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	if err := p.PublishBookingCreated(context.Background(), sampleEvent()); err != nil {
		t.Fatalf("PublishBookingCreated() error = %v", err)
	}
	if !strings.Contains(buf.String(), `"bookingId":"bk-1"`) {
		t.Fatalf("log = %s, want bookingId", buf.String())
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPubSubPublisher publishes through a real Pub/Sub client against the
// in-process fake server and checks the wire payload and trace attributes.
func TestPubSubPublisher(t *testing.T) {
	ctx := context.Background()
	srv := pstest.NewServer()
	defer srv.Close()
	if _, err := srv.GServer.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/p/topics/booking-created"}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial fake: %v", err)
	}
	defer conn.Close()

	otel.SetTextMapPropagator(propagation.TraceContext{})
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))

	p, err := NewPubSubPublisher(ctx, "p", "booking-created", option.WithGRPCConn(conn))
	if err != nil {
		t.Fatalf("NewPubSubPublisher() error = %v", err)
	}
	if err := p.PublishBookingCreated(ctx, sampleEvent()); err != nil {
		t.Fatalf("PublishBookingCreated() error = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	var got map[string]any
	if err := json.Unmarshal(msgs[0].Data, &got); err != nil {
		t.Fatalf("payload %q: %v", msgs[0].Data, err)
	}
	want := map[string]any{
		"bookingId": "bk-1", "venueId": "grand-baie-lagoon-deck", "venueName": "Lagoon Deck", "date": "2026-10-04",
		"guests": float64(10), "owner": "alice@example.mu", "contactEmail": "contact@example.mu", "createdAt": "2026-10-03T10:00:00Z",
	}
	if len(got) != len(want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, got[k], v)
		}
	}
	if tp := msgs[0].Attributes["traceparent"]; !strings.Contains(tp, "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Errorf("traceparent attribute = %q, want the request trace id", tp)
	}
	if msgs[0].Attributes["eventType"] != "booking-created" {
		t.Errorf("eventType attribute = %q", msgs[0].Attributes["eventType"])
	}
}
