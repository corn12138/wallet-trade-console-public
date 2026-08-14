package indexer

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Log is the minimal eth_getLogs / eth_subscribe log shape the parser
// consumes. Mirrors viem's Log type slice we actually use.
//
// Address / Topics / Data are lower-case 0x-prefixed hex.
// BlockNumber / LogIndex are decoded by the caller (indexer.service.ts
// reads them off the raw JSON).
type Log struct {
	Address string
	Topics  []string
	Data    string
}

// ParsedIndexedLog mirrors ParsedIndexedLog in
// legacy NestJS indexer/indexer-log-parser.ts.
//
//   - EventName is the Solidity name from EventSpec, or "Unknown" when
//     topic0 doesn't match a registered event.
//   - RuntimeArgs holds typed values (string for addresses, *big.Int
//     for uint*). Used by the in-process event handlers.
//   - PersistedArgs is the JSON-safe form (big.Int → decimal string).
//     This is what the NestJS service stores in the Web3Event.args JSON
//     column, so the parity matrix is byte-for-byte at the persist
//     boundary.
//   - ActorAddress is the lower-case 0x address derived from the
//     event-specific actor field (e.g. owner for Approval, sender for
//     Swap/Mint/Burn). Falls back to topics[1] when no spec rule fires.
type ParsedIndexedLog struct {
	EventName     string
	RuntimeArgs   map[string]any
	PersistedArgs map[string]any
	ActorAddress  string
}

// ParseIndexedLog mirrors parseIndexedLog() in indexer-log-parser.ts.
// When topic0 doesn't match a registered event OR decoding fails, the
// fallback record carries the raw topics + data through to the
// persistence layer (so we still record the row, just without typed
// args). This matches the NestJS try/catch fallback.
func ParseIndexedLog(log Log) ParsedIndexedLog {
	if len(log.Topics) == 0 {
		return unknownLog(log)
	}
	spec := EventSpecByTopic0(strings.ToLower(log.Topics[0]))
	if spec == nil {
		return unknownLog(log)
	}

	runtimeArgs, err := decodeEventArgs(spec, log)
	if err != nil {
		return unknownLog(log)
	}

	persisted := serializeJSONRecord(runtimeArgs)
	return ParsedIndexedLog{
		EventName:     spec.Name,
		RuntimeArgs:   runtimeArgs,
		PersistedArgs: persisted,
		ActorAddress:  extractActorAddress(spec, runtimeArgs, log),
	}
}

func unknownLog(log Log) ParsedIndexedLog {
	persisted := map[string]any{
		"topics": append([]string(nil), log.Topics...),
		"data":   log.Data,
	}
	actor := ""
	if len(log.Topics) > 1 {
		actor = addressFromTopic(log.Topics[1])
	}
	return ParsedIndexedLog{
		EventName:     "Unknown",
		RuntimeArgs:   map[string]any{},
		PersistedArgs: persisted,
		ActorAddress:  actor,
	}
}

// decodeEventArgs splits the spec's inputs into indexed (from
// topics[1..]) and non-indexed (from data) and decodes each.
func decodeEventArgs(spec *EventSpec, log Log) (map[string]any, error) {
	out := make(map[string]any, len(spec.Inputs))

	// Indexed: each consumes one topic slot, starting at topics[1].
	indexedCount := 0
	for _, in := range spec.Inputs {
		if !in.Indexed {
			continue
		}
		indexedCount++
		idx := indexedCount // topics[indexedCount]
		if idx >= len(log.Topics) {
			return nil, fmt.Errorf("topics truncated: need %d, have %d", idx+1, len(log.Topics))
		}
		v, err := decodeTopicValue(in.Type, log.Topics[idx])
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", spec.Name, in.Name, err)
		}
		out[in.Name] = v
	}

	// Non-indexed: ABI-encoded in log.data, in declaration order.
	dataBytes, err := decodeHex(log.Data)
	if err != nil {
		return nil, fmt.Errorf("data hex: %w", err)
	}

	staticInputs := make([]EventInput, 0, len(spec.Inputs))
	for _, in := range spec.Inputs {
		if !in.Indexed {
			staticInputs = append(staticInputs, in)
		}
	}
	values, err := decodeStaticAndDynamic(staticInputs, dataBytes)
	if err != nil {
		return nil, fmt.Errorf("%s data: %w", spec.Name, err)
	}
	for i, in := range staticInputs {
		out[in.Name] = values[i]
	}
	return out, nil
}

// decodeTopicValue decodes a single 32-byte topic word into its Solidity
// type. Indexed parameters wider than 32 bytes (strings, bytes, arrays)
// store the keccak of the value, not the value itself — our supported
// indexed types are all word-sized.
func decodeTopicValue(solType, topicHex string) (any, error) {
	raw, err := decodeHex(topicHex)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("topic must be 32 bytes, got %d", len(raw))
	}
	switch solType {
	case "address":
		return "0x" + hex.EncodeToString(raw[12:]), nil
	case "uint256", "uint112", "uint":
		return new(big.Int).SetBytes(raw), nil
	default:
		return nil, fmt.Errorf("unsupported indexed type %q", solType)
	}
}

// decodeStaticAndDynamic walks the non-indexed inputs. Static types take
// one 32-byte word; dynamic types (string) store a 32-byte offset
// pointing into the tail of the buffer where length-prefixed UTF-8
// bytes live, ABI-padded to 32-byte boundaries.
func decodeStaticAndDynamic(inputs []EventInput, data []byte) ([]any, error) {
	out := make([]any, len(inputs))
	cursor := 0
	for i, in := range inputs {
		if cursor+32 > len(data) {
			return nil, fmt.Errorf("input %d (%s) out of range at byte %d", i, in.Name, cursor)
		}
		word := data[cursor : cursor+32]
		switch in.Type {
		case "address":
			out[i] = "0x" + hex.EncodeToString(word[12:])
		case "uint256", "uint112", "uint":
			out[i] = new(big.Int).SetBytes(word)
		case "int256":
			out[i] = decodeInt256(word)
		case "bool":
			out[i] = word[31] != 0
		case "bytes32":
			out[i] = "0x" + hex.EncodeToString(word)
		case "string":
			off := new(big.Int).SetBytes(word)
			if !off.IsInt64() {
				return nil, fmt.Errorf("input %d (%s): string offset overflow", i, in.Name)
			}
			start := int(off.Int64())
			if start < 0 || start+32 > len(data) {
				return nil, fmt.Errorf("input %d (%s): string offset out of range", i, in.Name)
			}
			lenWord := data[start : start+32]
			n := new(big.Int).SetBytes(lenWord)
			if !n.IsInt64() {
				return nil, fmt.Errorf("input %d (%s): string length overflow", i, in.Name)
			}
			strLen := int(n.Int64())
			body := start + 32
			if strLen < 0 || body+strLen > len(data) {
				return nil, fmt.Errorf("input %d (%s): string body truncated", i, in.Name)
			}
			out[i] = string(data[body : body+strLen])
		default:
			return nil, fmt.Errorf("input %d (%s): unsupported type %q", i, in.Name, in.Type)
		}
		cursor += 32
	}
	return out, nil
}

func decodeHex(s string) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return []byte{}, nil
	}
	return hex.DecodeString(s)
}

// decodeInt256 interprets a 32-byte word as a two's-complement signed
// integer (Solidity int256). Needed for DecreasePosition/LiquidatePosition
// pnl, which is negative on losing positions.
func decodeInt256(word []byte) *big.Int {
	v := new(big.Int).SetBytes(word)
	if len(word) == 32 && word[0]&0x80 != 0 {
		max := new(big.Int).Lsh(big.NewInt(1), 256)
		v.Sub(v, max)
	}
	return v
}

// extractActorAddress walks the spec's ActorField first, then falls back
// to topics[1] (same precedence as indexer-log-parser.ts). For Transfer,
// if from is the zero address (mint event), fall back to "to".
func extractActorAddress(spec *EventSpec, args map[string]any, log Log) string {
	if spec.ActorField != "" {
		if v, ok := args[spec.ActorField].(string); ok && isLikelyAddress(v) {
			if spec.Name == "Transfer" && isZeroAddress(v) {
				if to, ok := args["to"].(string); ok && isLikelyAddress(to) {
					return strings.ToLower(to)
				}
			}
			return strings.ToLower(v)
		}
	}
	if len(log.Topics) > 1 {
		return addressFromTopic(log.Topics[1])
	}
	return ""
}

func addressFromTopic(topic string) string {
	raw, err := decodeHex(topic)
	if err != nil || len(raw) != 32 {
		return ""
	}
	return "0x" + hex.EncodeToString(raw[12:])
}

func isLikelyAddress(s string) bool {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") {
		return false
	}
	for _, c := range s[2:] {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

func isZeroAddress(s string) bool {
	return strings.EqualFold(s, "0x0000000000000000000000000000000000000000")
}

// serializeJSONRecord maps each value into a JSON-safe representation.
// *big.Int becomes its decimal string (matches the NestJS bigint
// serialization). Address strings stay lowercase. Anything else is
// passed through unchanged.
func serializeJSONRecord(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = serializeJSONValue(v)
	}
	return out
}

func serializeJSONValue(v any) any {
	switch x := v.(type) {
	case *big.Int:
		if x == nil {
			return "0"
		}
		return x.String()
	case string:
		if isLikelyAddress(x) {
			return strings.ToLower(x)
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = serializeJSONValue(item)
		}
		return out
	case map[string]any:
		return serializeJSONRecord(x)
	default:
		return v
	}
}
