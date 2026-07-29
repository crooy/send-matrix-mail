package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"send-matrix-mail/internal/config"
	"send-matrix-mail/internal/sendmail"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)

	c := &Client{
		homeserver:  srv.URL,
		userID:      "@bot:test",
		password:    "testpass",
		httpClient:  srv.Client(),
		joinedRooms: map[string]string{},
		tokenDir:    t.TempDir(),
	}
	return c, srv
}

func TestNewClient(t *testing.T) {
	var loginAttempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_matrix/client/v3/login" {
			loginAttempts++
			json.NewEncoder(w).Encode(map[string]string{
				"access_token": "test_token_123",
				"device_id":    "TESTDEVICE",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := config.MatrixConfig{
		Homeserver:  srv.URL,
		UserID:      "@bot:test",
		Password:    "testpass",
		StateDir:    t.TempDir(),
		DefaultRoom: "#general:test",
	}

	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected client, got nil")
	}
	if c.accessToken != "test_token_123" {
		t.Errorf("access_token: got %q", c.accessToken)
	}
	if loginAttempts != 1 {
		t.Errorf("expected 1 login attempt, got %d", loginAttempts)
	}

	// Second client should use cached token (no login)
	loginAttempts = 0
	c2, err := NewClient(config.MatrixConfig{
		Homeserver:  srv.URL,
		UserID:      "@bot:test",
		Password:    "",
		StateDir:    c.tokenDir,
		DefaultRoom: "#general:test",
	})
	if err != nil {
		t.Fatalf("NewClient cached: %v", err)
	}
	if loginAttempts != 0 {
		t.Errorf("expected 0 login attempts (cached), got %d", loginAttempts)
	}
	_ = c2
}

func TestSendToRoom(t *testing.T) {
	var requests []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		w.Header().Set("Content-Type", "application/json")

		// Accept join for default room
		if strings.Contains(r.URL.Path, "/join/%23general%3Atest") {
			json.NewEncoder(w).Encode(map[string]string{
				"room_id": "!defaultroom:test",
			})
			return
		}
		// Accept join for the recipient room
		if strings.Contains(r.URL.Path, "/join/") {
			json.NewEncoder(w).Encode(map[string]string{
				"room_id": "!room1:test",
			})
			return
		}
		// Accept send
		if strings.Contains(r.URL.Path, "/send/") {
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			if body["msgtype"] != "m.text" {
				t.Errorf("expected msgtype m.text, got %v", body["msgtype"])
			}
			// Check mentions are present for user-like recipients
			mentions, hasMentions := body["m.mentions"]
			if !hasMentions {
				t.Errorf("expected m.mentions in content")
			} else {
				m := mentions.(map[string]interface{})
				userIDs := m["user_ids"].([]interface{})
				if len(userIDs) != 1 {
					t.Errorf("expected 1 user_id in mentions, got %d", len(userIDs))
				}
				if userIDs[0].(string) != "@user1:test" {
					t.Errorf("expected mention @user1:test, got %v", userIDs[0])
				}
			}
			json.NewEncoder(w).Encode(map[string]string{
				"event_id": "$event123:test",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer srv.Close()

	c := &Client{
		homeserver:  srv.URL,
		httpClient:  srv.Client(),
		joinedRooms: map[string]string{},
		tokenDir:    t.TempDir(),
		defaultRoom: "#general:test",
	}

	// Set a valid token
	c.accessToken = "testtoken"
	c.saveToken()

	env := &sendmail.Envelope{
		Author:     "alice@example.com",
		Recipients: []string{"user1@test"},
		Subject:    "Hello",
		Headers:    "From: alice@example.com\r\nTo: user1@test\r\nSubject: Hello\r\n",
		Body:       "This is a test message.",
	}

	err := c.Send(context.Background(), env)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSendNoDefaultRoom(t *testing.T) {
	c := &Client{
		homeserver:  "http://invalid",
		httpClient:  http.DefaultClient,
		joinedRooms: map[string]string{},
		tokenDir:    t.TempDir(),
		accessToken: "testtoken",
	}

	// No default room set - should error
	env := &sendmail.Envelope{
		Recipients: []string{"user@test"},
		Body:       "test",
	}

	err := c.Send(context.Background(), env)
	if err == nil {
		t.Fatal("expected error for missing default room, got nil")
	}
	de, ok := err.(*DeliveryError)
	if !ok {
		t.Fatalf("expected DeliveryError, got %T: %v", err, err)
	}
	if de.Code != 67 {
		t.Errorf("expected code 67 (EX_NOUSER), got %d", de.Code)
	}
}

func TestSendTransientError(t *testing.T) {
	var attemptCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"errcode": "M_UNKNOWN",
			"error":   "server is having a bad day",
		})
	}))
	defer srv.Close()

	c := &Client{
		homeserver:  srv.URL,
		httpClient:  srv.Client(),
		joinedRooms: map[string]string{},
		tokenDir:    t.TempDir(),
		accessToken: "testtoken",
		defaultRoom: "#general:test",
	}

	env := &sendmail.Envelope{
		Author:     "bot@test",
		Recipients: []string{"room@test"},
		Body:       "test",
	}

	err := c.Send(context.Background(), env)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	de, ok := err.(*DeliveryError)
	if !ok {
		t.Fatalf("expected DeliveryError, got %T: %v", err, err)
	}
	if !de.Retryable() {
		t.Errorf("expected retryable error, got non-retryable: %v", de)
	}
}

func TestSendPermanentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Resolve default room succeeds
		if strings.Contains(r.URL.Path, "/join/%23general%3Atest") {
			json.NewEncoder(w).Encode(map[string]string{
				"room_id": "!defaultroom:test",
			})
			return
		}
		// Send fails with forbidden
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"errcode": "M_FORBIDDEN",
			"error":   "not in room",
		})
	}))
	defer srv.Close()

	c := &Client{
		homeserver:  srv.URL,
		httpClient:  srv.Client(),
		joinedRooms: map[string]string{},
		tokenDir:    t.TempDir(),
		accessToken: "testtoken",
		defaultRoom: "#general:test",
	}

	env := &sendmail.Envelope{
		Author:     "bot@test",
		Recipients: []string{"room@test"},
		Body:       "test",
	}

	err := c.Send(context.Background(), env)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	de, ok := err.(*DeliveryError)
	if !ok {
		t.Fatalf("expected DeliveryError, got %T: %v", err, err)
	}
	if de.Retryable() {
		t.Errorf("expected non-retryable error, got retryable: %v", de)
	}
}

func TestFormatMessage(t *testing.T) {
	env := &sendmail.Envelope{
		Author:     "alice@example.com",
		Recipients: []string{"bob@example.com"},
		Subject:    "Test",
		Headers:    "From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Test\r\n",
		Body:       "Hello, world!",
	}

	formatted := formatMessage(env, []string{"@bob:example.com"})
	if !strings.Contains(formatted, "From:") || !strings.Contains(formatted, "alice@example.com") {
		t.Errorf("missing From header")
	}
	if !strings.Contains(formatted, "Hello, world!") {
		t.Errorf("missing body")
	}
	if !strings.Contains(formatted, "Mention:") || !strings.Contains(formatted, "@bob:example.com") {
		t.Errorf("missing mention line")
	}

	html := formatMessageHTML(env, []string{"@bob:example.com"})
	if !strings.Contains(html, "<b>From:</b>") || !strings.Contains(html, "alice@example.com") {
		t.Errorf("HTML missing From header")
	}
	if !strings.Contains(html, "<br>") {
		t.Errorf("HTML missing line breaks")
	}
	if !strings.Contains(html, "@bob:example.com") {
		t.Errorf("HTML missing mention")
	}
}

func TestFormatMessageNoMentions(t *testing.T) {
	env := &sendmail.Envelope{
		Author:  "alice@example.com",
		Subject: "Test",
		Headers: "From: alice@example.com\r\nSubject: Test\r\n",
		Body:    "Hello, world!",
	}

	formatted := formatMessage(env, nil)
	if strings.Contains(formatted, "Mention:") {
		t.Errorf("unexpected mention line when no mentions")
	}

	html := formatMessageHTML(env, nil)
	if strings.Contains(html, "matrix.to") {
		t.Errorf("unexpected mention link when no mentions")
	}
}

func TestMentionUserID(t *testing.T) {
	tests := []struct {
		addr     string
		expected string
	}{
		{"user@host", "@user:host"},
		{"alice@example.com", "@alice:example.com"},
		{"#room@host", ""},                 // room alias prefix -> not a user mention
		{"barelocalpart", ""},              // no @
		{"@already:host", ""}, // already has @ prefix — not valid user@domain
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := mentionUserID(tt.addr); got != tt.expected {
				t.Errorf("mentionUserID(%q) = %q, want %q", tt.addr, got, tt.expected)
			}
		})
	}
}

func TestIsTransient(t *testing.T) {
	tests := []struct {
		errcode  string
		expected bool
	}{
		{"M_LIMIT_EXCEEDED", true},
		{"M_UNKNOWN", true},
		{"M_SERVER_NOT_REACHABLE", true},
		{"M_FORBIDDEN", false},
		{"M_UNKNOWN_TOKEN", false},
		{"M_NOT_FOUND", false},
		{"M_BAD_JSON", false},
	}
	for _, tt := range tests {
		t.Run(tt.errcode, func(t *testing.T) {
			if got := isTransient(tt.errcode); got != tt.expected {
				t.Errorf("isTransient(%q) = %v, want %v", tt.errcode, got, tt.expected)
			}
		})
	}
}