package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

func TestActivationAndRollbackBothRefuseChangedCodeBeforePointerWrite(t *testing.T) {
	valid := cliSnapshot(t)
	for _, command := range []string{"activate", "rollback"} {
		t.Run(command, func(t *testing.T) {
			reg := &cliRegistry{snapshot: valid}
			var stdout, stderr bytes.Buffer
			status := run([]string{command, "--revision", "1", "--expected-activation-version", "4", "--actor", "proof", "--reason", "code change refusal"}, &stdout, &stderr, env{
				open: func(context.Context) (registry, func(), error) { return reg, func() {}, nil },
				code: func([]int) (executionregistry.CodeReader, error) { return cliCode{changed: true}, nil },
			})
			if status != 1 || reg.activations != 0 || !strings.Contains(stderr.String(), "CODE_CHANGED") {
				t.Fatalf("%s status=%d activations=%d diagnostic=%s", command, status, reg.activations, stderr.String())
			}
		})
	}
}
