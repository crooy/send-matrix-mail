// send-matrix-mail is a sendmail-compatible Matrix client.
// Reads RFC 5322 email from stdin, resolves recipients to Matrix delivery targets,
// and posts an m.text message to each unique room. Queues locally when offline.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"send-matrix-mail/internal/config"
	"send-matrix-mail/internal/matrix"
	"send-matrix-mail/internal/queue"
	"send-matrix-mail/internal/sendmail"
)

// version is set at build time via -ldflags. See Makefile.
var version = "dev"

const subjectDefault = "[send-matrix-mail]"

func main() {
	var dryRun bool
	var verbose bool
	var logPath string
	var filteredArgs []string

	// Early flags: --version, -v, -n, -l (before config loading)
	// These are stripped before passing remaining args to sendmail.Parse.
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--version":
			fmt.Println("send-matrix-mail", version)
			return
		case a == "--":
			filteredArgs = append(filteredArgs, a)
			filteredArgs = append(filteredArgs, args[i+1:]...)
			break
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "-n" || a == "--dry-run":
			dryRun = true
		case a == "-l" && i+1 < len(args):
			i++
			logPath = args[i]
		default:
			filteredArgs = append(filteredArgs, a)
		}
	}

	// Rebuild os.Args[1:] from filtered list for sendmail.Parse
	cleanArgs := append([]string{os.Args[0]}, filteredArgs...)

	// Redirect log output to a file if requested
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			log.Fatalf("log file: %v", err)
		}
		defer f.Close()
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}

	// Extract -C <path> from args (sendmail compat flag) before passing to sendmail.Parse
	configPath := extractConfigPath(os.Args)

	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("config: %v", err)
		os.Exit(78) // EX_CONFIG
	}

	// Parse sendmail args + stdin
	env, err := sendmail.Parse(cleanArgs, os.Stdin, cfg.Author)
	if err != nil {
		var se *sendmail.Error
		if errors.As(err, &se) {
			os.Exit(se.Code)
		}
		os.Exit(65) // EX_DATAERR
	}

	if dryRun {
		fmt.Println("--- dry run ---")
		fmt.Printf("From:    %s\n", env.Author)
		fmt.Printf("To:      %s\n", strings.Join(env.Recipients, ", "))
		fmt.Printf("Subject: %s\n", env.Subject)
		fmt.Printf("Config:  %s\n", configPath)
		fmt.Printf("Spool:   %s\n", cfg.SpoolDir)
		fmt.Printf("Room:    %s\n", cfg.Matrix.DefaultRoom)
		fmt.Println("--- OK (dry run, no message sent) ---")
		return
	}

	// Default subject when missing
	if env.Subject == "" {
		env.Subject = subjectDefault
	}

	// Create matrix client
	client, err := matrix.NewClient(cfg.Matrix)
	if err != nil {
		log.Printf("matrix: %v", err)
		os.Exit(78) // EX_CONFIG
	}

	// Create spool
	spool := queue.NewSpool(cfg.SpoolDir)

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Deliver — the one deep call. The spool owns the delivery lifecycle.
	if err := spool.Deliver(ctx, env, client.Send); err != nil {
		log.Printf("send-matrix-mail: %v", err)
		os.Exit(73) // EX_CANTCREAT
	}

	if verbose {
		msg := fmt.Sprintf("delivered to %s (recipients: %s, subject: %s)",
			cfg.Matrix.DefaultRoom,
			strings.Join(env.Recipients, ", "),
			env.Subject)
		log.Print(msg)
	}
}

// extractConfigPath finds -C <path> in args and returns the path, or "".
func extractConfigPath(args []string) string {
	for i, a := range args {
		if a == "-C" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}