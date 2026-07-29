// send-matrix-mail is a sendmail-compatible Matrix client.
// Reads RFC 5322 email from stdin, resolves recipients to Matrix delivery targets,
// and posts an m.text message to each unique room. Queues locally when offline.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"send-matrix-mail/internal/config"
	"send-matrix-mail/internal/matrix"
	"send-matrix-mail/internal/queue"
	"send-matrix-mail/internal/sendmail"
)

// version is set at build time via -ldflags. See Makefile.
var version = "dev"

func main() {
	// --version flag (must come before config loading so it works without config)
	for _, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Println("send-matrix-mail", version)
			return
		}
		if a == "--" {
			break
		}
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
	env, err := sendmail.Parse(os.Args, os.Stdin, cfg.Author)
	if err != nil {
		var se *sendmail.Error
		if errors.As(err, &se) {
			os.Exit(se.Code)
		}
		os.Exit(65) // EX_DATAERR
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