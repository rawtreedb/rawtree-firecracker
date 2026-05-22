package observability

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Collector struct {
	client  *http.Client
	mu      sync.Mutex
	request SandboxLaunchRequest
}

func NewCollector(request SandboxLaunchRequest) *Collector {
	return &Collector{
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				ForceAttemptHTTP2:     false,
				IdleConnTimeout:       30 * time.Second,
				ResponseHeaderTimeout: 10 * time.Second,
				TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
				TLSHandshakeTimeout:   5 * time.Second,
			},
		},
		request: request,
	}
}

func (c *Collector) Record(event Event) error {
	return c.RecordMany([]Event{event})
}

func (c *Collector) RecordMany(events []Event) error {
	if len(events) == 0 {
		return nil
	}

	enrichedEvents := make([]Event, 0, len(events))
	for _, event := range events {
		enrichedEvents = append(enrichedEvents, c.enrich(event))
	}

	var payload []byte
	var err error
	if len(enrichedEvents) == 1 {
		payload, err = json.Marshal(enrichedEvents[0])
	} else {
		payload, err = json.Marshal(enrichedEvents)
	}
	if err != nil {
		return fmt.Errorf("marshal RawTree event: %w", err)
	}

	url := strings.TrimRight(c.request.RawTree.BaseURL, "/") +
		"/v1/tables/" + c.request.RawTree.Table
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create RawTree request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.request.RawTree.APIKey)
	req.Header.Set("Content-Type", "application/json")

	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("insert RawTree event: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		return fmt.Errorf("RawTree insert failed (%d): %s", resp.StatusCode, truncate(body.String(), 500))
	}

	return nil
}

func (c *Collector) Flush() error {
	return nil
}

func (c *Collector) enrich(event Event) Event {
	eventTime, ok := event["event_time"].(string)
	if !ok || eventTime == "" {
		eventTime = time.Now().UTC().Format(time.RFC3339Nano)
	}

	enriched := Event{}
	for key, value := range event {
		enriched[key] = value
	}

	if _, ok := enriched["event_id"]; !ok {
		enriched["event_id"] = uuid.NewString()
	}
	enriched["event_time"] = eventTime
	if _, ok := enriched["metadata"]; !ok {
		enriched["metadata"] = c.request.Metadata
	}
	enriched["provider"] = c.request.Provider
	enriched["run_id"] = c.request.RunID
	if _, ok := enriched["sampled_at"]; !ok {
		enriched["sampled_at"] = eventTime
	}
	enriched["sandbox_id"] = c.request.SandboxID
	if _, ok := enriched["source"]; !ok {
		enriched["source"] = "firecracker_host_collector"
	}

	return enriched
}

func ErrorFields(err error) map[string]string {
	if err == nil {
		return map[string]string{}
	}

	return map[string]string{
		"error_message": err.Error(),
		"error_name":    fmt.Sprintf("%T", err),
	}
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
