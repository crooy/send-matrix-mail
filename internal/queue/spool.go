// Package queue implements a local filesystem spool for offline delivery.
// The only public entry point: Spool.Deliver — tries, enqueues on transient failure,
// then processes pending messages up to a deadline.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"send-matrix-mail/internal/sendmail"
)

// Retryable is implemented by errors that indicate a transient failure.
type Retryable interface {
	error
	Retryable() bool
}

// SendFunc delivers an envelopeelope to Matrix. Errors implementing Retryable
// signal transient failure; all others are permanent.
type SendFunc func(ctx context.Context, envelope *sendmail.Envelope) error

// meta is the per-message retry state persisted as a JSON sidecar.
type meta struct {
	ID          string   `json:"id"`
	QueuedAt    int64    `json:"queued_at"`    // unix nano
	Attempts    int      `json:"attempts"`
	NextAttempt int64    `json:"next_attempt"` // unix nano
	ExpiresAt   int64    `json:"expires_at"`   // unix nano
	LastError   string   `json:"last_error,omitempty"`
	LastAttempt int64    `json:"last_attempt,omitempty"`
	Author      string   `json:"author"`
	Recipients  []string `json:"recipients"`
	Size        int      `json:"size"`
}

const (
	defaultBackoffBase = 60 * time.Second
	defaultBackoffCap  = 24 * time.Hour
	defaultLifetime    = 7 * 24 * time.Hour
	defaultDeadline    = 60 * time.Second
)

// Spool is a local filesystem queue for offline delivery resilience.
type Spool struct {
	dir       string
	mu        sync.Mutex
	backoff   time.Duration
	backoffCap time.Duration
	lifetime  time.Duration
	deadline  time.Duration
}

// NewSpool creates a spool rooted at dir, creating directories as needed.
func NewSpool(dir string) *Spool {
	return &Spool{
		dir:        dir,
		backoff:    defaultBackoffBase,
		backoffCap: defaultBackoffCap,
		lifetime:   defaultLifetime,
		deadline:   defaultDeadline,
	}
}

// Deliver attempts to send envelope via sendFn. On transient failure, enqueues to the
// local spool and processes pending messages up to the deadline.
// Returns nil if the message was delivered OR safely queued.
// Returns error only if even local queueing fails (disk full, permissions).
func (s *Spool) Deliver(ctx context.Context, env *sendmail.Envelope, sendFn SendFunc) error {
	err := sendFn(ctx, env)
	if err == nil {
		s.processDue(ctx, sendFn)
		return nil
	}

	var re Retryable
	if errors.As(err, &re) && !re.Retryable() {
		return err
	}

	if enqueueErr := s.enqueue(env, err.Error()); enqueueErr != nil {
		return fmt.Errorf("queue write failed (original: %v): %w", err, enqueueErr)
	}

	s.processDue(ctx, sendFn)
	return nil
}

// enqueue atomically writes the envelopeelope to the spool.
func (s *Spool) enqueue(envelope *sendmail.Envelope, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Join(s.dir, "tmp"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "queue"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "failed"), 0700); err != nil {
		return err
	}

	id := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	now := time.Now()

	// Write body file to tmp
	bodyPath := filepath.Join(s.dir, "tmp", id)
	var body strings.Builder
	body.WriteString("author: ")
	body.WriteString(envelope.Author)
	body.WriteString("\n")
	body.WriteString("recipients: ")
	body.WriteString(strings.Join(envelope.Recipients, ", "))
	body.WriteString("\n\n")
	body.WriteString(envelope.Headers)
	body.WriteString("\n")
	body.WriteString(envelope.Body)

	if err := os.WriteFile(bodyPath, []byte(body.String()), 0600); err != nil {
		return err
	}

	// Write meta sidecar to tmp
	m := meta{
		ID:          id,
		QueuedAt:    now.UnixNano(),
		Attempts:    1,
		NextAttempt: now.Add(s.backoff).UnixNano(),
		ExpiresAt:   now.Add(s.lifetime).UnixNano(),
		LastError:   lastError,
		LastAttempt: now.UnixNano(),
		Author:      envelope.Author,
		Recipients:  envelope.Recipients,
		Size:        len(body.String()),
	}
	metaBytes, err := json.Marshal(m)
	if err != nil {
		os.Remove(bodyPath)
		return err
	}

	metaPath := filepath.Join(s.dir, "tmp", id+".meta")
	if err := os.WriteFile(metaPath, metaBytes, 0600); err != nil {
		os.Remove(bodyPath)
		return err
	}

	// Atomic commit: fsync both files, rename to queue/, fsync queue dir
	if err := fsyncFile(bodyPath); err != nil {
		os.Remove(bodyPath)
		os.Remove(metaPath)
		return err
	}
	if err := fsyncFile(metaPath); err != nil {
		os.Remove(bodyPath)
		os.Remove(metaPath)
		return err
	}

	qBodyPath := filepath.Join(s.dir, "queue", id)
	if err := os.Rename(bodyPath, qBodyPath); err != nil {
		os.Remove(bodyPath)
		os.Remove(metaPath)
		return err
	}
	qMetaPath := filepath.Join(s.dir, "queue", id+".meta")
	if err := os.Rename(metaPath, qMetaPath); err != nil {
		os.Remove(qBodyPath)
		os.Remove(metaPath)
		return err
	}

	// fsync the queue directory to make the new entries durable
	return fsyncDir(filepath.Join(s.dir, "queue"))
}

// processDue delivers pending spool messages that are due for retry, up to the deadline.
func (s *Spool) processDue(ctx context.Context, sendFn SendFunc) {
	deadline := time.Now().Add(s.deadline)
	s.process(ctx, deadline, sendFn)
}

// process iterates over the queue, attempting delivery for due items until deadline.
func (s *Spool) process(ctx context.Context, deadline time.Time, sendFn SendFunc) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "queue"))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("queue: read dir: %v", err)
		}
		return
	}

	// Collect unique message IDs (dedupe body + .meta entries)
	type msg struct {
		id   string
		body string
		meta meta
	}
	var msgs []msg
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".meta") {
			continue
		}
		id := name
		if seen[id] {
			continue
		}
		seen[id] = true

		// Read meta
		metaBytes, err := os.ReadFile(filepath.Join(s.dir, "queue", id+".meta"))
		if err != nil {
			continue
		}
		var m meta
		if err := json.Unmarshal(metaBytes, &m); err != nil {
			continue
		}
		msgs = append(msgs, msg{id: id, meta: m})
	}
	// Sort by next_attempt so we process oldest-eligible first
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].meta.NextAttempt < msgs[j].meta.NextAttempt
	})

	for _, m := range msgs {
		if time.Now().After(deadline) {
			break
		}
		if m.meta.NextAttempt > time.Now().UnixNano() {
			continue // not yet due
		}
		if m.meta.ExpiresAt > 0 && time.Now().UnixNano() > m.meta.ExpiresAt {
			// Expired — move to failed
			s.fail(m.id, "expired after max lifetime")
			continue
		}

		// Reconstruct envelopeelope from stored body
		bodyPath := filepath.Join(s.dir, "queue", m.id)
		bodyBytes, err := os.ReadFile(bodyPath)
		if err != nil {
			continue
		}
		envelope, err := parseBody(bodyBytes)
		if err != nil {
			s.fail(m.id, fmt.Sprintf("corrupt body: %v", err))
			continue
		}

		err = sendFn(ctx, envelope)
		if err == nil {
			// Delivered — remove from spool
			os.Remove(bodyPath)
			os.Remove(filepath.Join(s.dir, "queue", m.id+".meta"))
			continue
		}

		// Error handling
		var re Retryable
		if errors.As(err, &re) && !re.Retryable() {
			// Permanent failure
			s.fail(m.id, err.Error())
			continue
		}

		// Transient — update and retry later
		m.meta.Attempts++
		backoff := time.Duration(math.Min(
			float64(s.backoff)*math.Pow(2, float64(m.meta.Attempts-1)),
			float64(s.backoffCap),
		))
		m.meta.NextAttempt = time.Now().Add(backoff).UnixNano()
		m.meta.LastError = err.Error()
		m.meta.LastAttempt = time.Now().UnixNano()

		// Atomic rewrite of meta
		metaBytes, _ := json.Marshal(m.meta)
		tmpMeta := filepath.Join(s.dir, "tmp", m.id+".meta")
		os.WriteFile(tmpMeta, metaBytes, 0600)
		fsyncFile(tmpMeta)
		os.Rename(tmpMeta, filepath.Join(s.dir, "queue", m.id+".meta"))
		fsyncDir(filepath.Join(s.dir, "queue"))
	}
}

// fail moves a queued message to the failed directory.
func (s *Spool) fail(id, reason string) {
	bodyPath := filepath.Join(s.dir, "queue", id)
	metaPath := filepath.Join(s.dir, "queue", id+".meta")
	failBody := filepath.Join(s.dir, "failed", id)
	failMeta := filepath.Join(s.dir, "failed", id+".meta")

	// Update meta with final error
	if b, err := os.ReadFile(metaPath); err == nil {
		var m meta
		if json.Unmarshal(b, &m) == nil {
			m.LastError = reason
			m.LastAttempt = time.Now().UnixNano()
			if newB, err := json.Marshal(m); err == nil {
				// Write updated meta to tmp, then rename
				tmpMeta := filepath.Join(s.dir, "tmp", id+".meta")
				os.WriteFile(tmpMeta, newB, 0600)
				os.Rename(tmpMeta, failMeta)
			}
		}
	}

	os.Rename(bodyPath, failBody)
	os.Rename(metaPath, failMeta) // best-effort if already moved above
}

func fsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func fsyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// parseBody reads an Envelope from the stored body format.
func parseBody(b []byte) (*sendmail.Envelope, error) {
	s := string(b)
	lines := strings.SplitN(s, "\n\n", 2)
	if len(lines) < 2 {
		return nil, fmt.Errorf("missing header/body separator")
	}
	headerLines := strings.Split(lines[0], "\n")
	envelope := &sendmail.Envelope{}
	for _, line := range headerLines {
		if strings.HasPrefix(line, "author: ") {
			envelope.Author = strings.TrimPrefix(line, "author: ")
		} else if strings.HasPrefix(line, "recipients: ") {
			rcpts := strings.TrimPrefix(line, "recipients: ")
			envelope.Recipients = strings.Split(rcpts, ", ")
		}
	}
	body := lines[1]
	// The stored format has headers then body after \n\n
	if idx := strings.Index(body, "\n\n"); idx >= 0 {
		envelope.Headers = body[:idx]
		envelope.Body = body[idx+2:]
	} else {
		envelope.Body = body
	}
	return envelope, nil
}