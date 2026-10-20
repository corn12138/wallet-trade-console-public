package outboxrun

import (
	"context"
	"errors"
	"testing"
)

func TestDisabledRuntimeNeverOpensDatabase(t *testing.T) {
	for _, value := range []string{"", "false"} {
		cfg, err := Load(func(key string) string {
			if key == "CAMPAIGN_OUTBOX_ENABLED" {
				return value
			}
			return "invalid ignored while disabled"
		})
		if err != nil || cfg.Enabled || Run(context.Background(), "invalid secret database string", cfg, nil) != nil {
			t.Fatal("disabled runtime attempted startup")
		}
	}
}

func TestRuntimeConfigurationRequiresExplicitBoundedValues(t *testing.T) {
	for _, overrides := range []map[string]string{
		{"CAMPAIGN_OUTBOX_ENABLED": "TRUE"},
		{"CAMPAIGN_OUTBOX_BATCH": "5"},
		{"CAMPAIGN_OUTBOX_BATCH": "01"},
		{"CAMPAIGN_OUTBOX_POLL_MS": "0"},
		{"CAMPAIGN_OUTBOX_POLL_MS": "60001"},
	} {
		_, err := Load(func(key string) string {
			if value, ok := overrides[key]; ok {
				return value
			}
			if key == "CAMPAIGN_OUTBOX_ENABLED" {
				return "true"
			}
			return ""
		})
		if !errors.Is(err, ErrConfig) {
			t.Fatalf("invalid config accepted: %v", overrides)
		}
	}
}
