package v2

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type wireCase struct {
	Name, Contract, Raw, Reason string
}

func wireCases(t *testing.T) []wireCase {
	t.Helper()
	raw, err := os.ReadFile("../../../../../packages/shared/contracts/fixtures/campaign-v2-wire-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []wireCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func wireSample(t *testing.T, contract string) string {
	t.Helper()
	for _, item := range wireCases(t) {
		if item.Name == "valid "+contract {
			return item.Raw
		}
	}
	t.Fatal("missing shared wire fixture: " + contract)
	return ""
}

func TestSharedCampaignWireCorpus(t *testing.T) {
	for _, item := range wireCases(t) {
		t.Run(item.Name, func(t *testing.T) {
			var output any
			decode := DecodeContract
			if strings.HasPrefix(item.Contract, "Query") || item.Contract == "CommandResponse" {
				decode = DecodeContractResponse
			}
			err := decode(item.Contract, []byte(item.Raw), &output)
			if item.Reason == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			code := "CAMPAIGN_INVALID_INPUT"
			if item.Reason == "unsupported" {
				code = "CAMPAIGN_UNSUPPORTED"
			}
			errorCode(t, err, code)
		})
	}
}

func TestCampaignWireGeneratedTargets(t *testing.T) {
	for name, target := range map[string]any{
		"CreateCommandInput": &CreateCommandInput{}, "RevisionCommandInput": &RevisionCommandInput{},
		"PublicationCommandInput": &PublicationCommandInput{}, "PositiveInteractionInput": &PositiveInteractionInput{},
		"CancelInteractionInput": &CancelInteractionInput{}, "QueryListResponse": &QueryListResponse{},
		"QueryDetailResponse": &QueryDetailResponse{}, "QueryMeResponse": &QueryMeResponse{},
		"QueryHistoryResponse": &QueryHistoryResponse{}, "QueryPreviewResponse": &QueryPreviewResponse{},
		"CommandResponse": &CommandResponse{},
	} {
		t.Run(name, func(t *testing.T) {
			if err := DecodeContractResponse(name, []byte(wireSample(t, name)), target); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(target)
			if err != nil {
				t.Fatal(err)
			}
			var redecoded any
			// Required nil pointers must round-trip as explicit null; omitempty
			// would silently change the generated response contract.
			if err := DecodeContractResponse(name, raw, &redecoded); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCampaignWireBudgets(t *testing.T) {
	for _, item := range []struct {
		name   string
		limit  int
		decode func(string, []byte, any) error
	}{{"CancelInteractionInput", 64 * 1024, DecodeContract}, {"QueryMeResponse", 1024 * 1024, DecodeContractResponse}} {
		t.Run(item.name, func(t *testing.T) {
			source := wireSample(t, item.name)
			raw := []byte(source + strings.Repeat(" ", item.limit-len(source)))
			var result any
			if err := item.decode(item.name, raw, &result); err != nil {
				t.Fatal(err)
			}
			errorCode(t, item.decode(item.name, append(raw, ' '), &result), "CAMPAIGN_INVALID_INPUT")
		})
	}
	var result any
	raw := []byte(wireSample(t, "CreateCommandInput"))
	raw = append(raw, 0xff)
	errorCode(t, DecodeContract("CreateCommandInput", raw, &result), "CAMPAIGN_INVALID_INPUT")
	errorCode(t, DecodeContract("CancelInteractionInput", []byte(wireSample(t, "CancelInteractionInput")), nil), "CAMPAIGN_INVALID_INPUT")
}

func TestCampaignRawJSONDepthBoundary(t *testing.T) {
	for _, depth := range []int{24, 25} {
		decoder := json.NewDecoder(strings.NewReader(strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)))
		decoder.UseNumber()
		_, err := decodeUniqueJSON(decoder, 0)
		if (err == nil) != (depth == 24) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
}
