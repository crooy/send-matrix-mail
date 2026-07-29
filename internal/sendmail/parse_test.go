package sendmail

import (
	"os"
	"strings"
	"testing"

	"send-matrix-mail/internal/config"
)

func TestParseBasic(t *testing.T) {
	args := []string{"send-matrix-mail", "alice@example.com"}
	stdin := strings.NewReader("From: bob@example.com\r\nTo: alice@example.com\r\nSubject: Test\r\n\r\nHello, world.\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if env.Author != "bob@example.com" {
		t.Errorf("author: got %q, want %q", env.Author, "bob@example.com")
	}
	if len(env.Recipients) != 1 || env.Recipients[0] != "alice@example.com" {
		t.Errorf("recipients: got %v, want [alice@example.com]", env.Recipients)
	}
	if env.Subject != "Test" {
		t.Errorf("subject: got %q, want %q", env.Subject, "Test")
	}
	if env.Body != "Hello, world.\n" {
		t.Errorf("body: got %q", env.Body)
	}
}

func TestParseWithTFlag(t *testing.T) {
	args := []string{"send-matrix-mail", "-t"}
	stdin := strings.NewReader("From: bob@example.com\r\nTo: alice@example.com\r\nCc: carol@example.com\r\nSubject: Test\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(env.Recipients) != 2 {
		t.Errorf("recipients count: got %d, want 2: %v", len(env.Recipients), env.Recipients)
	}
}

func TestParseWithBccStripped(t *testing.T) {
	args := []string{"send-matrix-mail", "-t"}
	stdin := strings.NewReader("From: bob@example.com\r\nTo: alice@example.com\r\nBcc: secret@example.com\r\nSubject: Test\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Bcc should be extracted into recipients but stripped from headers
	if !contains(env.Recipients, "secret@example.com") {
		t.Errorf("Bcc not in recipients: %v", env.Recipients)
	}
	if strings.Contains(env.Headers, "Bcc") {
		t.Errorf("Bcc header not stripped: %s", env.Headers)
	}
}

func TestParseWithFFlag(t *testing.T) {
	args := []string{"send-matrix-mail", "-f", "sender@override.com", "alice@example.com"}
	stdin := strings.NewReader("From: bob@example.com\r\nSubject: Test\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if env.Author != "sender@override.com" {
		t.Errorf("author: got %q, want override", env.Author)
	}
}

func TestParseCompatFlagsIgnored(t *testing.T) {
	// All msmtp compat flags should parse without error
	args := []string{"send-matrix-mail", "-bm", "-G", "-m", "-n", "-U", "-v",
		"-B", "8BITMIME", "-h", "5", "-L", "tag", "-N", "never",
		"-R", "full", "-V", "envid", "-A", "mode", "-p", "proto",
		"-O", "opt=val", "-o", "x", "val",
		"recipient@example.com"}
	stdin := strings.NewReader("Subject: Test\r\nFrom: sender@example.com\r\n\r\nBody\n")

	_, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse with compat flags: %v", err)
	}
}

func TestParseEmailEnvFallback(t *testing.T) {
	t.Setenv("EMAIL", "envuser@example.com")
	args := []string{"send-matrix-mail", "alice@example.com"}
	stdin := strings.NewReader("Subject: Test\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if env.Author != "envuser@example.com" {
		t.Errorf("author: got %q, want %q", env.Author, "envuser@example.com")
	}
}

func TestParseUserEnvFallback(t *testing.T) {
	t.Setenv("EMAIL", "")
	t.Setenv("USER", "testuser")
	args := []string{"send-matrix-mail", "alice@example.com"}
	stdin := strings.NewReader("Subject: Test\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if !strings.Contains(env.Author, "testuser@") {
		t.Errorf("author: got %q, expected user@host", env.Author)
	}
}

func TestParseConfigDefaultFrom(t *testing.T) {
	t.Setenv("EMAIL", "")
	t.Setenv("USER", "")
	t.Setenv("LOGNAME", "")
	args := []string{"send-matrix-mail", "alice@example.com"}
	stdin := strings.NewReader("Subject: Test\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{DefaultFrom: "cron@example.com"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if env.Author != "cron@example.com" {
		t.Errorf("author: got %q, want config default", env.Author)
	}
}

func TestParseNoAuthor(t *testing.T) {
	t.Setenv("EMAIL", "")
	t.Setenv("USER", "")
	t.Setenv("LOGNAME", "")
	args := []string{"send-matrix-mail", "alice@example.com"}
	stdin := strings.NewReader("Subject: Test\r\n\r\nBody\n")

	_, err := Parse(args, stdin, config.AuthorConfig{})
	if err == nil {
		t.Fatal("expected error for missing author")
	}
	se, ok := err.(*Error)
	if !ok || se.Code != 78 {
		t.Errorf("expected EX_CONFIG (78), got %v", err)
	}
}

func TestParseNoRecipients(t *testing.T) {
	args := []string{"send-matrix-mail", "-t"}
	stdin := strings.NewReader("Subject: Test\r\n\r\nBody\n")

	_, err := Parse(args, stdin, config.AuthorConfig{})
	if err == nil {
		t.Fatal("expected error for no recipients")
	}
	se, ok := err.(*Error)
	if !ok || se.Code != 64 {
		t.Errorf("expected EX_USAGE (64), got %v", err)
	}
}

func TestParseResentHeaders(t *testing.T) {
	args := []string{"send-matrix-mail", "-t"}
	stdin := strings.NewReader("From: original@example.com\r\n" +
		"Resent-From: forwarder@example.com\r\n" +
		"Resent-To: forwarded@example.com\r\n" +
		"Subject: Fwd\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Resent-From should take precedence over From for author
	if env.Author != "forwarder@example.com" {
		t.Errorf("author: got %q, want %q", env.Author, "forwarder@example.com")
	}
}

func TestParseEmptyStdin(t *testing.T) {
	args := []string{"send-matrix-mail", "alice@example.com"}
	stdin := strings.NewReader("")

	env, err := Parse(args, stdin, config.AuthorConfig{DefaultFrom: "cron@host"})
	if err != nil {
		t.Fatalf("Parse empty stdin: %v", err)
	}
	if env.Body != "" {
		t.Errorf("expected empty body")
	}
}

func TestParseDeduplicateRecipients(t *testing.T) {
	args := []string{"send-matrix-mail", "alice@example.com", "alice@example.com", "bob@example.com"}
	stdin := strings.NewReader("From: sender@example.com\r\nTo: alice@example.com\r\n\r\nBody\n")

	env, err := Parse(args, stdin, config.AuthorConfig{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(env.Recipients) != 2 {
		t.Errorf("expected 2 unique recipients, got %d: %v", len(env.Recipients), env.Recipients)
	}
}

func TestLicense(_ *testing.T) {
	// This file exists — that's the license.
}

func contains(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
