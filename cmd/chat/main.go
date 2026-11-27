// Command chat is a terminal REPL for manually testing the conversation engine.
//
// It creates a test user with saved places (ghar, mandir) in a real Postgres
// database, then lets you type messages and see replies and the current state.
//
// Usage:
//
//	go run ./cmd/chat
//
// Environment:
//
//	DATABASE_URL          Postgres connection string (required)
//	ANTHROPIC_API_KEY     If set, uses Claude for intent extraction.
//	                      Otherwise uses the rule-based FakeExtractor.
//
// Safety: the database name must NOT end in a production-like name. The tool
// refuses to run if DATABASE_URL points at a database whose name contains
// "prod". It also never logs credentials.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ankitnegi-dev/Sahayak/internal/config"
	"github.com/ankitnegi-dev/Sahayak/internal/conversation"
	"github.com/ankitnegi-dev/Sahayak/internal/llm"
	"github.com/ankitnegi-dev/Sahayak/internal/safety"
	"github.com/ankitnegi-dev/Sahayak/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "chat: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.DB.URL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	// Safety: refuse to run against a production database.
	if strings.Contains(strings.ToLower(cfg.DB.URL), "prod") {
		return fmt.Errorf("refusing to run against a production database")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DB.URL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// Clean up test data from previous runs.
	pool.Exec(ctx, "DELETE FROM orders WHERE user_id IN (SELECT id FROM users WHERE phone = '+919999999999')")
	pool.Exec(ctx, "DELETE FROM conversations WHERE user_id IN (SELECT id FROM users WHERE phone = '+919999999999')")
	pool.Exec(ctx, "DELETE FROM saved_places WHERE user_id IN (SELECT id FROM users WHERE phone = '+919999999999')")
	pool.Exec(ctx, "DELETE FROM users WHERE phone = '+919999999999'")

	// Create test user with saved places.
	var userID string
	err = pool.QueryRow(ctx,
		"INSERT INTO users (phone, role, language) VALUES ('+919999999999', 'parent', 'hi') RETURNING id::text").Scan(&userID)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	// Create a child user with an active family link.
	var childUserID string
	err = pool.QueryRow(ctx,
		"INSERT INTO users (phone, role, language) VALUES ('+918888888888', 'child', 'hi') RETURNING id::text").Scan(&childUserID)
	if err != nil {
		return fmt.Errorf("create child: %w", err)
	}
	pool.Exec(ctx, "INSERT INTO family_links (child_id, parent_id, status) VALUES ($1, $2, 'active')", childUserID, userID)

	places := map[string]string{"ghar": "123 Main Street", "mandir": "Temple Road"}
	for label, addr := range places {
		_, err := pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, $2, $3)", userID, label, addr)
		if err != nil {
			return fmt.Errorf("create place %s: %w", label, err)
		}
	}

	// Choose extractor.
	var extractor llm.Extractor
	if cfg.Secrets.AnthropicAPIKey != "" {
		extractor = llm.NewClaude(llm.ClaudeConfig{APIKey: cfg.Secrets.AnthropicAPIKey})
		fmt.Println("Using Claude for intent extraction")
	} else {
		extractor = llm.FakeExtractor{}
		fmt.Println("Using FakeExtractor (set ANTHROPIC_API_KEY for Claude)")
	}

	store := store.NewStore(pool)
	safetyChecker := safety.NewChecker(store, conversation.RealClock{}, safety.DefaultConfig())
	engine := conversation.NewEngine(store, extractor, &conversation.FakeBooker{}, safetyChecker, &conversation.FakeNotifier{}, nil)

	fmt.Printf("Test user created: %s\n", userID)
	fmt.Println("Saved places: ghar, mandir")
	fmt.Println("Type messages (or 'quit' to exit):")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if text == "quit" || text == "exit" {
			break
		}

		// Handle /approve and /deny commands (acting as child).
		if strings.HasPrefix(text, "/approve ") {
			code := strings.TrimSpace(strings.TrimPrefix(text, "/approve "))
			approval, err := engine.DecideApproval(ctx, childUserID, code, true)
			if err != nil {
				fmt.Printf("ERROR: %v\n", err)
			} else {
				fmt.Printf("Approved: %s (pickup: %s, drop: %s)\n", approval.ShortCode, approval.Pickup, approval.Drop)
				// Move the parent's conversation back to awaiting_confirmation.
				// The engine will pick this up on the next message.
			}
			continue
		}
		if strings.HasPrefix(text, "/deny ") {
			code := strings.TrimSpace(strings.TrimPrefix(text, "/deny "))
			approval, err := engine.DecideApproval(ctx, childUserID, code, false)
			if err != nil {
				fmt.Printf("ERROR: %v\n", err)
			} else {
				fmt.Printf("Denied: %s\n", approval.ShortCode)
			}
			continue
		}
		if text == "/limits" {
			// Show limits.
			fmt.Println("Limits: (not yet implemented)")
			continue
		}
		if strings.HasPrefix(text, "/killswitch") {
			fmt.Println("Kill switch: (not yet implemented)")
			continue
		}
		if text == "/reset" {
			pool.Exec(ctx, "UPDATE conversations SET state = 'idle', context = '{}' WHERE user_id = $1", userID)
			pool.Exec(ctx, "DELETE FROM orders WHERE user_id = $1", userID)
			fmt.Println("Conversation reset to idle.")
			continue
		}

		reply, err := engine.Handle(ctx, userID, text)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			continue
		}
		fmt.Printf("Bot: %s\n", reply.Text)

		// Show current state.
		var state string
		pool.QueryRow(ctx, "SELECT state FROM conversations WHERE user_id = $1", userID).Scan(&state)
		fmt.Printf("  [state: %s]\n", state)
	}

	fmt.Println("Goodbye!")
	return nil
}
