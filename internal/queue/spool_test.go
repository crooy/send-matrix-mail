package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"send-matrix-mail/internal/sendmail"
)

// testSendFn creates a SendFunc with controlled behavior.
func testSendFn(failCount *int, err error) SendFunc {
	return func(ctx context.Context, env *sendmail.Envelope) error {
		if failCount != nil && *failCount > 0 {
			*failCount--
			return err
		}
		return nil
	}
}

type testErr struct {
	msg       string
	retryable bool
}

func (e *testErr) Error() string { return e.msg }
func (e *testErr) Retryable() bool { return e.retryable }

func TestDeliverSuccess(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)

	env := &sendmail.Envelope{
		Author: "test@example.com",
		Recipients: []string{"alice@example.com"},
		Subject: "Test",
		Body: "Hello",
	}

	var calls int
	err := s.Deliver(context.Background(), env, func(ctx context.Context, e *sendmail.Envelope) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 send call, got %d", calls)
	}

	// No files in queue
	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	if len(entries) > 0 {
		t.Errorf("expected empty queue, got %d items", len(entries))
	}
}

func TestDeliverTransientEnqueue(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)

	env := &sendmail.Envelope{
		Author: "test@example.com",
		Recipients: []string{"alice@example.com"},
		Subject: "Retry",
		Body: "Will be queued",
	}

	fails := 1
	err := s.Deliver(context.Background(), env, testSendFn(&fails, &testErr{
		msg:       "network timeout",
		retryable: true,
	}))
	if err != nil {
		t.Fatalf("Deliver should succeed (queued): %v", err)
	}

	// Message should be in the queue
	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	bodyCount := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".meta") {
			bodyCount++
		}
	}
	if bodyCount != 1 {
		t.Errorf("expected 1 queued message, got %d", bodyCount)
	}
}

func TestDeliverPermanentError(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)

	env := &sendmail.Envelope{
		Author: "test@example.com",
		Recipients: []string{"bogus@example.com"},
		Body: "Will fail permanently",
	}

	err := s.Deliver(context.Background(), env, func(ctx context.Context, e *sendmail.Envelope) error {
		return &testErr{msg: "room not found", retryable: false}
	})
	if err == nil {
		t.Fatal("expected permanent error, got nil")
	}

	// Should NOT be queued
	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	if len(entries) > 0 {
		t.Errorf("expected empty queue for permanent error, got %d items", len(entries))
	}
}

func TestProcessDue(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	s.backoff = time.Nanosecond // retry immediately

	// Manually queue a message
	env := &sendmail.Envelope{
		Author: "sender@example.com",
		Recipients: []string{"rcpt@example.com"},
		Body: "Process me",
		Headers: "From: sender@example.com\r\nTo: rcpt@example.com\r\n",
	}
	if err := s.enqueue(env, "first attempt failed"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var deliverCount int
	s.processDue(context.Background(), func(ctx context.Context, e *sendmail.Envelope) error {
		deliverCount++
		return nil
	})
	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	if len(entries) > 0 {
		t.Errorf("expected queue empty after delivery")
	}
}

func TestProcessDueWithBackoff(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	s.backoff = time.Nanosecond

	env := &sendmail.Envelope{
		Author: "sender@example.com",
		Recipients: []string{"rcpt@example.com"},
		Body: "Will retry",
	}
	s.enqueue(env, "down")

	s.processDue(context.Background(), func(ctx context.Context, e *sendmail.Envelope) error {
		return nil
	})

	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	if len(entries) > 0 {
		t.Errorf("expected queue empty after successful retry")
	}
}

func TestExpiry(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	s.lifetime = time.Millisecond // expire immediately
	s.backoff = time.Nanosecond   // due immediately

	env := &sendmail.Envelope{
		Author: "old@example.com",
		Recipients: []string{"gone@example.com"},
		Body: "Too old",
	}
	if err := s.enqueue(env, "queued"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Wait a tiny bit for expiry
	time.Sleep(10 * time.Millisecond)

	// processDue should see it's expired and move to failed
	s.processDue(context.Background(), func(ctx context.Context, e *sendmail.Envelope) error {
		return nil
	})

	failDir := filepath.Join(dir, "failed")
	entries, _ := os.ReadDir(failDir)
	if len(entries) == 0 {
		t.Error("expected failed dir to have the expired message")
	}
}

func TestProcessWithSendFn_TransientThenSuccess(t *testing.T) {
	dir := t.TempDir()
	s := NewSpool(dir)
	s.backoff = time.Nanosecond

	env := &sendmail.Envelope{
		Author: "retry@example.com",
		Recipients: []string{"dest@example.com"},
		Body: "Kept failing",
	}

	if err := s.enqueue(env, "first fail"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// processDue with success
	s.process(context.Background(), time.Now().Add(10*time.Second), func(ctx context.Context, e *sendmail.Envelope) error {
		return nil
	})

	entries, _ := os.ReadDir(filepath.Join(dir, "queue"))
	if len(entries) > 0 {
		t.Errorf("expected queue empty after successful process")
	}
}

func TestRetryableInterface(t *testing.T) {
	var re Retryable
	re = &testErr{msg: "transient", retryable: true}
	if !re.Retryable() {
		t.Error("expected retryable")
	}

	re = &testErr{msg: "permanent", retryable: false}
	if re.Retryable() {
		t.Error("expected non-retryable")
	}
}

func TestParseBody(t *testing.T) {
	body := `author: sender@example.com
recipients: a@b.c, d@e.f

From: sender@example.com
To: a@b.c

Hello world`

	env, err := parseBody([]byte(body))
	if err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	if env.Author != "sender@example.com" {
		t.Errorf("author: got %q", env.Author)
	}
	if len(env.Recipients) != 2 {
		t.Errorf("recipients: got %d", len(env.Recipients))
	}
	if env.Headers != "From: sender@example.com\nTo: a@b.c" {
		t.Errorf("headers: got %q", env.Headers)
	}
	if env.Body != "Hello world" {
		t.Errorf("body: got %q", env.Body)
	}
}

func TestParseBodyEmpty(t *testing.T) {
	_, err := parseBody([]byte("no double newline"))
	if err == nil {
		t.Error("expected error for missing separator")
	}
}