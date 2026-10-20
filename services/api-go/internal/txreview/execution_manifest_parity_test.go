package txreview

import (
	"encoding/hex"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

func TestExecutionDecoderSignaturesMatchRegistryManifest(t *testing.T) {
	manifest := executionregistry.BuiltinManifest()
	for id, signature := range map[string]string{
		"perp.positionManager.open.v1":  openPositionSignature,
		"perp.positionManager.close.v1": closePositionSignature,
	} {
		iface, exists := manifest.Interfaces[id]
		selector := "0x" + hex.EncodeToString(abi.Selector(signature))
		if !exists || iface.Signature != signature || iface.Selector != selector {
			t.Errorf("decoder/manifest drift for %s: decoder=%s/%s manifest=%s/%s", id, signature, selector, iface.Signature, iface.Selector)
		}
	}
}
