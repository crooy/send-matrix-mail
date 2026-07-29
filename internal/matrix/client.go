// Package matrix wraps the Matrix client-server API for sending messages.
// Uses raw HTTP (no external SDK) — send-only, 3 endpoints.
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"send-matrix-mail/internal/config"
	"send-matrix-mail/internal/sendmail"
)

// DeliveryError carries error information with a retryable flag.
type DeliveryError struct {
	RetryableFlag bool
	Code          int    // sendmail exit code
	Msg           string // human-readable description
}

func (e *DeliveryError) Error() string { return e.Msg }

// Retryable returns true if the error is transient and should be retried.
func (e *DeliveryError) Retryable() bool { return e.RetryableFlag }

// Client is a send-only Matrix client.
type Client struct {
	homeserver  string
	userID      string
	accessToken string
	deviceID    string
	password    string
	defaultRoom string

	// runtime state
	httpClient *http.Client
	tokenDir   string
	joinedRooms map[string]string // alias or room_id → canonical room_id
}

// NewClient creates a Matrix client. If no cached token exists, it logs in.
func NewClient(cfg config.MatrixConfig) (*Client, error) {
	tokenDir := defaultStateDir()
	if cfg.StateDir != "" {
		tokenDir = cfg.StateDir
	}

	c := &Client{
		homeserver:  strings.TrimRight(cfg.Homeserver, "/"),
		userID:      cfg.UserID,
		password:    cfg.Password,
		defaultRoom: cfg.DefaultRoom,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		tokenDir:    tokenDir,
		joinedRooms: make(map[string]string),
	}

	if c.defaultRoom == "" {
		return nil, &DeliveryError{RetryableFlag: false, Code: 78, Msg: "default_room is required"}
	}

	// Try loading cached token
	if err := c.loadToken(); err == nil && c.accessToken != "" {
		return c, nil
	}

	// Fall back to config token
	if cfg.AccessToken != "" {
		c.accessToken = cfg.AccessToken
		return c, nil
	}

	// No cached or config token — try password login
	if c.password == "" {
		return nil, &DeliveryError{RetryableFlag: false, Code: 78, Msg: "no access token and no password configured"}
	}
	if err := c.login(); err != nil {
		return nil, err
	}
	if err := c.saveToken(); err != nil {
		return nil, err
	}

	return c, nil
}

// Send resolves the default room and sends the message, mentioning any
// user recipients alongside any explicitly targeted rooms.
func (c *Client) Send(ctx context.Context, env *sendmail.Envelope) error {
	// Resolve default room once
	defaultID, err := c.resolveRoomID(ctx, c.defaultRoom)
	if err != nil {
		return err
	}

	// Collect explicitly targeted rooms (via #localpart:domain resolution)
	targets := make(map[string]bool)
	for _, recipient := range env.Recipients {
		roomID, rErr := c.resolveRecipient(ctx, recipient)
		if rErr != nil {
			if de, ok := rErr.(*DeliveryError); ok && de.Retryable() {
				return rErr
			}
			continue
		}
		targets[roomID] = true
	}

	// Always deliver to the default room
	targets[defaultID] = true

	if len(targets) == 0 {
		return &DeliveryError{RetryableFlag: false, Code: 67, Msg: "no deliverable targets"}
	}

	// Collect mentions from recipients who aren't rooms
	var mentions []string
	for _, recipient := range env.Recipients {
		userID := mentionUserID(recipient)
		if userID != "" {
			mentions = append(mentions, userID)
		}
	}

	plainText := formatMessage(env, mentions)
	htmlText := formatMessageHTML(env, mentions)
	for roomID := range targets {
		if sErr := c.sendText(ctx, roomID, plainText, htmlText, mentions); sErr != nil {
			return sErr
		}
	}
	return nil
}

// mentionUserID returns a Matrix user ID if the address looks like a
// user@domain, or empty string if it's a bare localpart or room alias.
func mentionUserID(addr string) string {
	localpart, domain, ok := strings.Cut(addr, "@")
	if !ok || localpart == "" || domain == "" {
		return ""
	}
	// If it starts with # it's a room alias, not a user mention
	if strings.HasPrefix(localpart, "#") {
		return ""
	}
	return "@" + localpart + ":" + domain
}

// resolveRecipient maps a recipient address to a Matrix room ID.
// Tries room alias first, then falls back to the default room.
// Never creates DM rooms.
func (c *Client) resolveRecipient(ctx context.Context, addr string) (string, error) {
	localpart, domain, ok := strings.Cut(addr, "@")
	if !ok {
		return "", &DeliveryError{RetryableFlag: false, Code: 67, Msg: fmt.Sprintf("invalid recipient: %q", addr)}
	}

	// 1. Try room #localpart:domain
	roomAlias := "#" + localpart + ":" + domain
	roomID, err := c.joinRoom(ctx, roomAlias)
	if err == nil && roomID != "" {
		return roomID, nil
	}
	if de, ok := err.(*DeliveryError); ok && de.Retryable() {
		return "", err
	}

	// 2. Fallback to default room
	return c.resolveRoomID(ctx, c.defaultRoom)
}

// joinRoom joins a room by alias and returns the canonical room ID.
func (c *Client) joinRoom(ctx context.Context, roomAlias string) (string, error) {
	apiURL := c.homeserver + "/_matrix/client/v3/join/" + url.PathEscape(roomAlias)
	var resp struct {
		RoomID  string `json:"room_id"`
		ErrCode string `json:"errcode"`
		Error   string `json:"error"`
	}
	if err := c.doRequestRaw(ctx, "POST", apiURL, struct{}{}, &resp); err != nil {
		return "", err
	}
	if resp.ErrCode != "" {
		if resp.ErrCode == "M_NOT_FOUND" {
			return "", &DeliveryError{
				RetryableFlag: false,
				Msg:           fmt.Sprintf("room %q not found", roomAlias),
			}
		}
		return "", &DeliveryError{
			RetryableFlag: resp.ErrCode == "M_LIMIT_EXCEEDED" || resp.ErrCode == "M_TOO_LARGE",
			Msg:           fmt.Sprintf("join room: %s %s", resp.ErrCode, resp.Error),
		}
	}
	c.joinedRooms[roomAlias] = resp.RoomID
	c.joinedRooms[resp.RoomID] = resp.RoomID
	return resp.RoomID, nil
}

// resolveRoomID resolves a room ID or alias to a canonical room ID.
func (c *Client) resolveRoomID(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", &DeliveryError{RetryableFlag: false, Code: 67, Msg: "no default room configured"}
	}
	// Already canonical?
	if strings.HasPrefix(id, "!") {
		return id, nil
	}
	// Already resolved?
	if resolved, ok := c.joinedRooms[id]; ok {
		return resolved, nil
	}
	return c.joinRoom(ctx, id)
}

// sendText sends an m.text message to a room, with HTML formatting
// for clients that support org.matrix.custom.html, and user mentions.
func (c *Client) sendText(ctx context.Context, roomID, plainText, htmlText string, mentions []string) error {
	content := map[string]interface{}{
		"msgtype":        "m.text",
		"body":           plainText,
		"format":         "org.matrix.custom.html",
		"formatted_body":  htmlText,
	}

	if len(mentions) > 0 {
		content["m.mentions"] = map[string]interface{}{
			"user_ids": mentions,
		}
	}

	// Generate txnId: nanosecond timestamp + random suffix
	txnID := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	apiURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		c.homeserver, url.PathEscape(roomID), txnID)

	var resp struct {
		EventID string `json:"event_id"`
		ErrCode string `json:"errcode"`
		Error   string `json:"error"`
	}
	if err := c.doRequestRaw(ctx, "PUT", apiURL, content, &resp); err != nil {
		return err
	}
	if resp.ErrCode != "" {
		return &DeliveryError{
			RetryableFlag: isTransient(resp.ErrCode),
			Msg:           fmt.Sprintf("send: %s %s", resp.ErrCode, resp.Error),
		}
	}
	return nil
}

// isTransient returns true if the Matrix errcode indicates a transient failure.
func isTransient(errcode string) bool {
	switch errcode {
	case "M_LIMIT_EXCEEDED", "M_TOO_LARGE", "M_UNKNOWN", "M_SERVER_NOT_REACHABLE":
		return true
	case "M_UNKNOWN_TOKEN", "M_FORBIDDEN", "M_NOT_FOUND", "M_BAD_JSON", "M_INVALID_ROOM":
		return false
	default:
		// Most M_ errors are permanent
		return false
	}
}

// doRequest sends an authenticated JSON request to the homeserver API.
func (c *Client) doRequest(ctx context.Context, method, apiPath string, body interface{}, resp interface{}) error {
	apiURL := c.homeserver + apiPath
	return c.doRequestRaw(ctx, method, apiURL, body, resp)
}

// doRequestRaw sends an authenticated JSON request to an arbitrary URL.
func (c *Client) doRequestRaw(ctx context.Context, method, apiURL string, body interface{}, resp interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return &DeliveryError{RetryableFlag: false, Msg: fmt.Sprintf("marshal: %v", err)}
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiURL, reqBody)
	if err != nil {
		return &DeliveryError{RetryableFlag: false, Msg: fmt.Sprintf("request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return &DeliveryError{RetryableFlag: true, Msg: fmt.Sprintf("http: %v", err)}
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return &DeliveryError{RetryableFlag: true, Msg: fmt.Sprintf("read response: %v", err)}
	}

	if httpResp.StatusCode >= 500 {
		return &DeliveryError{
			RetryableFlag: true,
			Code:          httpResp.StatusCode,
			Msg:           fmt.Sprintf("server error %d: %s", httpResp.StatusCode, string(respBytes)),
		}
	}
	if httpResp.StatusCode >= 400 {
		// Try to parse the Matrix error
		var merr struct {
			ErrCode string `json:"errcode"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(respBytes, &merr) == nil && merr.ErrCode != "" {
			retryable := isTransient(merr.ErrCode)
			if merr.ErrCode == "M_UNKNOWN_TOKEN" {
				// Token expired: try re-login
				if c.password != "" {
					if loginErr := c.login(); loginErr == nil {
						if saveErr := c.saveToken(); saveErr == nil {
							// Retry once with new token
							return c.doRequestRaw(ctx, method, apiURL, body, resp)
						}
					}
				}
			}
			return &DeliveryError{
				RetryableFlag: retryable,
				Code:          httpResp.StatusCode,
				Msg:           fmt.Sprintf("matrix error %s: %s", merr.ErrCode, merr.Error),
			}
		}
		return &DeliveryError{
			RetryableFlag: httpResp.StatusCode == 429,
			Code:          httpResp.StatusCode,
			Msg:           fmt.Sprintf("http %d: %s", httpResp.StatusCode, string(respBytes)),
		}
	}

	if resp != nil {
		if err := json.Unmarshal(respBytes, resp); err != nil {
			return &DeliveryError{RetryableFlag: false, Msg: fmt.Sprintf("unmarshal response: %v", err)}
		}
	}
	return nil
}

// login authenticates with password and stores the access token.
func (c *Client) login() error {
	body := map[string]interface{}{
		"type": "m.login.password",
		"identifier": map[string]string{
			"type": "m.id.user",
			"user": c.userID,
		},
		"password": c.password,
	}
	type loginResponse struct {
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
		Error       string `json:"error"`
		ErrCode     string `json:"errcode"`
	}
	var resp loginResponse
	if err := c.doRequest(context.Background(), "POST", "/_matrix/client/v3/login", body, &resp); err != nil {
		return err
	}
	if resp.ErrCode != "" {
		return fmt.Errorf("login: %s %s", resp.ErrCode, resp.Error)
	}
	c.accessToken = resp.AccessToken
	c.deviceID = resp.DeviceID
	return nil
}

// tokenDir returns the directory for token storage.
func defaultStateDir() string {
	return config.DefaultStateDir()
}

// loadToken reads the cached access token from disk.
func (c *Client) loadToken() error {
	b, err := os.ReadFile(filepath.Join(c.tokenDir, "token.json"))
	if err != nil {
		return err
	}
	var data struct {
		AccessToken string `json:"access_token"`
		DeviceID    string `json:"device_id"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	if data.AccessToken == "" {
		return fmt.Errorf("empty access token")
	}
	c.accessToken = data.AccessToken
	c.deviceID = data.DeviceID
	return nil
}

// saveToken writes the access token to disk.
func (c *Client) saveToken() error {
	if err := os.MkdirAll(c.tokenDir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{
		"access_token": c.accessToken,
		"device_id":    c.deviceID,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.tokenDir, "token.json"), data, 0600)
}

// formatMessage builds a plain-text m.text message body with clean formatting.
func formatMessage(env *sendmail.Envelope, mentions []string) string {
	var b strings.Builder

	subject := env.Subject
	from := extractHeader(env.Headers, "From")
	to := extractHeader(env.Headers, "To")
	date := env.Date
	if date == "" {
		date = extractHeader(env.Headers, "Date")
	}

	// Mention line
	if len(mentions) > 0 {
		fmt.Fprintf(&b, "Mention: %s\n\n", strings.Join(mentions, " "))
	}

	// Header block with padding for alignment
	if from != "" {
		fmt.Fprintf(&b, "From:   %s\n", from)
	}
	if to != "" {
		fmt.Fprintf(&b, "To:     %s\n", to)
	}
	if subject != "" {
		fmt.Fprintf(&b, "Subject: %s\n", subject)
	}
	if date != "" {
		fmt.Fprintf(&b, "Date:   %s\n", date)
	}

	b.WriteString("\n")

	if env.Body != "" {
		indented := "  " + strings.ReplaceAll(env.Body, "\n", "\n  ")
		b.WriteString(indented)
	}

	return b.String()
}

// formatMessageHTML builds an HTML-formatted message body for Matrix clients
// that support org.matrix.custom.html formatting.
func formatMessageHTML(env *sendmail.Envelope, mentions []string) string {
	var b strings.Builder

	subject := env.Subject
	from := extractHeader(env.Headers, "From")
	to := extractHeader(env.Headers, "To")
	date := env.Date
	if date == "" {
		date = extractHeader(env.Headers, "Date")
	}

	// Mention line
	if len(mentions) > 0 {
		for _, userID := range mentions {
			escapedUser := htmlEscape(userID)
			fmt.Fprintf(&b, "<a href=\"https://matrix.to/#/%s\">%s</a> ", escapedUser, escapedUser)
		}
		b.WriteString("<br><br>")
	}

	// Header block with bold labels
	if from != "" {
		fmt.Fprintf(&b, "<b>From:</b>   %s<br>", htmlEscape(from))
	}
	if to != "" {
		fmt.Fprintf(&b, "<b>To:</b>     %s<br>", htmlEscape(to))
	}
	if subject != "" {
		fmt.Fprintf(&b, "<b>Subject:</b> %s<br>", htmlEscape(subject))
	}
	if date != "" {
		fmt.Fprintf(&b, "<b>Date:</b>   %s<br>", htmlEscape(date))
	}

	b.WriteString("<br>")

	if env.Body != "" {
		escaped := htmlEscape(env.Body)
		lines := strings.Split(escaped, "\n")
		for i, line := range lines {
			if i > 0 {
				b.WriteString("<br>")
			}
			b.WriteString("&nbsp;&nbsp;")
			b.WriteString(line)
		}
	}

	return b.String()
}

// extractHeader extracts a header value from raw RFC 5322 header text.
func extractHeader(headers, key string) string {
	prefix := key + ":"
	lines := strings.Split(headers, "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, prefix) {
			val := strings.TrimSpace(trimmed[len(prefix):])
			// Collect continuation lines (folded headers)
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				if next == "" || next == "\r" {
					break
				}
				if next[0] == ' ' || next[0] == '\t' {
					val += " " + strings.TrimSpace(next)
				} else {
					break
				}
			}
			return val
		}
	}
	return ""
}

// htmlEscape escapes HTML special characters.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}