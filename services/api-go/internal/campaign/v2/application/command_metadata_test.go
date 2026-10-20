package application

import (
	"strings"
	"testing"
)

func TestCommandReasonUsesCanonicalUnicodeLength(t *testing.T) {
	c := adminCommand("pause")
	c.Reason = strings.Repeat("变", 1000)
	if _, err := validateCommand(actorA, c); err != nil {
		t.Fatalf("valid multilingual reason rejected: %v", err)
	}
	c.Reason += "更"
	if _, err := validateCommand(actorA, c); err == nil {
		t.Fatal("overlong reason accepted")
	}
}
