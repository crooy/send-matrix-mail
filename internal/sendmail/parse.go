// Package sendmail parses sendmail-style CLI arguments and RFC 5322 messages from stdin.
// Single entry point: Parse(args, stdin, cfg) → *Envelope.
package sendmail

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"os"
	"strings"

	"send-matrix-mail/internal/config"
)

// Envelope holds the parsed message and routing information.
type Envelope struct {
	Author     string   // resolved author address
	Recipients []string // all recipients (argv + header extraction)
	Subject    string
	Date       string
	MessageID  string
	Headers    string // raw header block, Bcc stripped
	Body       string // message body
}

// Error carries a sysexits code.
type Error struct {
	Code int    // sysexits code
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Parse parses sendmail CLI arguments and an RFC 5322 message from stdin.
func Parse(args []string, stdin io.Reader, cfg config.AuthorConfig) (*Envelope, error) {
	flags, recipients, err := parseFlags(args)
	if err != nil {
		return nil, err
	}

	msgBytes, err := io.ReadAll(stdin)
	if err != nil {
		return nil, &Error{Code: 66, Msg: "cannot read stdin"}
	}

	// net/mail.ReadMessage rejects empty input, so handle it ourselves.
	env := &Envelope{}
	if len(bytes.TrimSpace(msgBytes)) > 0 {
		msg, err := mail.ReadMessage(bytes.NewReader(msgBytes))
		if err != nil {
			return nil, &Error{Code: 65, Msg: fmt.Sprintf("malformed message: %v", err)}
		}

		bodyBytes, err := io.ReadAll(msg.Body)
		if err != nil {
			return nil, &Error{Code: 65, Msg: fmt.Sprintf("cannot read body: %v", err)}
		}

		env.Subject = msg.Header.Get("Subject")
		env.Date = msg.Header.Get("Date")
		env.MessageID = msg.Header.Get("Message-Id")
		env.Body = string(bodyBytes)

		// Extract recipients from headers if -t
		if flags.ReadRecipients {
			recipients = dedupe(append(recipients, collectRecipients(msg.Header)...))
		}

		// Build header block without Bcc
		env.Headers = filterHeaders(msg.Header)

		// Resolve author
		author, err := resolveAuthor(flags.Author, msg.Header, cfg)
		if err != nil {
			return nil, err
		}
		env.Author = author
	} else {
		// Empty stdin: resolve author from env/config only
		author, err := resolveAuthor(flags.Author, mail.Header{}, cfg)
		if err != nil {
			return nil, err
		}
		env.Author = author
	}

	if len(recipients) == 0 && flags.ReadRecipients {
		return nil, &Error{Code: 64, Msg: "no recipients specified"}
	}

	env.Recipients = dedupe(recipients)
	return env, nil
}

// flags holds parsed sendmail flags.
type flags struct {
	Author         string
	ReadRecipients bool
}

// parseFlags walks argv extracting sendmail flags.
func parseFlags(args []string) (flags, []string, error) {
	var f flags
	var recipients []string
	i := 1 // skip argv[0]

	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			i++
			recipients = append(recipients, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			recipients = append(recipients, arg)
			i++
			continue
		}

		switch arg {
		case "-t":
			f.ReadRecipients = true
		case "-f":
			i++
			if i >= len(args) {
				return f, nil, &Error{Code: 64, Msg: "-f requires an argument"}
			}
			f.Author = args[i]
		case "-F":
			i++ // accept but ignore display name
		case "-i", "-oi":
			// no-op; always read to EOF
		case "-bm", "-G", "-m", "-n", "-U", "-v", "-bs":
			// accepted-but-ignored
		case "-B", "-h", "-L", "-N", "-R", "-V", "-A", "-p":
			i++ // skip argument
		case "-O":
			i++ // skip opt=val
		case "-o":
			i++
			if i < len(args) && !strings.HasPrefix(args[i], "-") {
				i++ // skip old-style option value
			}
		case "-q":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++ // skip optional queue interval
			}
		case "-d":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++ // skip optional debug level
			}
		default:
			// unknown flags silently ignored for max compat
		}
		i++
	}

	return f, recipients, nil
}

// collectRecipients extracts addresses from To, Cc, Bcc headers.
// If Resent-* headers exist, those take precedence (RFC 5322 §3.6.6).
func collectRecipients(h mail.Header) []string {
	if len(h["Resent-To"]) > 0 {
		return collectHeaderAddrs(h, "Resent-To", "Resent-Cc", "Resent-Bcc")
	}
	return collectHeaderAddrs(h, "To", "Cc", "Bcc")
}

func collectHeaderAddrs(h mail.Header, keys ...string) []string {
	var addrs []string
	for _, key := range keys {
		for _, v := range h[key] {
			list, err := mail.ParseAddressList(v)
			if err != nil {
				addrs = append(addrs, v)
				continue
			}
			for _, a := range list {
				addrs = append(addrs, a.Address)
			}
		}
	}
	return addrs
}

// resolveAuthor resolves the envelope author address.
func resolveAuthor(flagFrom string, h mail.Header, cfg config.AuthorConfig) (string, error) {
	if flagFrom != "" {
		return flagFrom, nil
	}
	for _, key := range []string{"Resent-From", "From"} {
		if v := h.Get(key); v != "" {
			addrs, err := mail.ParseAddressList(v)
			if err == nil && len(addrs) > 0 {
				return addrs[0].Address, nil
			}
			if strings.Contains(v, "@") {
				return strings.TrimSpace(v), nil
			}
		}
	}
	if email := os.Getenv("EMAIL"); email != "" {
		return email, nil
	}
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	if user != "" {
		host := cfg.DefaultHost
		if host == "" {
			var err error
			host, err = os.Hostname()
			if err != nil {
				host = "localhost"
			}
		}
		return user + "@" + host, nil
	}
	if cfg.DefaultFrom != "" {
		return cfg.DefaultFrom, nil
	}
	return "", &Error{Code: 78, Msg: "envelope-from address is missing"}
}

// filterHeaders returns the header block with Bcc removed.
func filterHeaders(h mail.Header) string {
	var b strings.Builder
	for key, vals := range h {
		if strings.EqualFold(key, "Bcc") {
			continue
		}
		for _, v := range vals {
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	return b.String()
}

func dedupe(addrs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}