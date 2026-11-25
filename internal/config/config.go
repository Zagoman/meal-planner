// Package config reads all settings from environment variables. Secrets
// (the Anthropic key, the session secret) are only ever read here and passed
// to the server side; they never reach templates or the browser.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	Addr            string // ADDR, default ":8080"
	DatabaseURL     string // DATABASE_URL, required
	BaseURL         string // BASE_URL, e.g. https://meals.liluhome.dk
	SessionSecret   string // SESSION_SECRET, required, at least 32 chars
	AnthropicAPIKey string // ANTHROPIC_API_KEY; empty disables plan generation
	ClaudeModel     string // CLAUDE_MODEL, required when the key is set
	AdminEmail      string // ADMIN_EMAIL: this member is the server admin
	ResendAPIKey    string // RESEND_API_KEY; empty: emails are only logged
	EmailFrom       string // EMAIL_FROM, default "Hvad skal vi ha'? <meals@liluhome.dk>"
	GenerateTimeout time.Duration
}

func Load() (Config, error) {
	c := Config{
		Addr:            env("ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		BaseURL:         strings.TrimRight(env("BASE_URL", "http://localhost:8080"), "/"),
		SessionSecret:   os.Getenv("SESSION_SECRET"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		ClaudeModel:     os.Getenv("CLAUDE_MODEL"),
		AdminEmail:      strings.ToLower(strings.TrimSpace(os.Getenv("ADMIN_EMAIL"))),
		ResendAPIKey:    os.Getenv("RESEND_API_KEY"),
		EmailFrom:       env("EMAIL_FROM", "Hvad skal vi ha'? <meals@liluhome.dk>"),
		GenerateTimeout: 55 * time.Second, // spec: a plan takes under 60 s
	}
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if len(c.SessionSecret) < 32 {
		errs = append(errs, errors.New("SESSION_SECRET must be at least 32 characters"))
	}
	if c.AnthropicAPIKey != "" && c.ClaudeModel == "" {
		errs = append(errs, errors.New("CLAUDE_MODEL is required when ANTHROPIC_API_KEY is set"))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
