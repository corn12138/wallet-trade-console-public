package postgres

import "encoding/json"

type readDiscriminator struct {
	path    []string
	strings []string
	version bool
}

// Future discriminators choose a different contract before any of its child
// fields are interpreted. Known discriminators still require the complete
// closed schema; this cannot hide an extra field in a currently supported rule.
func knownReadContract(envelope map[string]json.RawMessage, schema int64) (bool, error) {
	if schema != 1 {
		return false, nil
	}
	fields := []readDiscriminator{
		{path: []string{"templateId"}, strings: []string{"campaignCardV1"}},
		{path: []string{"materialTerms", "rules", "join", "kind"}, strings: []string{"walletJoinV1"}},
		{path: []string{"materialTerms", "rules", "join", "version"}, version: true},
		{path: []string{"materialTerms", "rules", "reminder", "kind"}, strings: []string{"reminderIntentV1"}},
		{path: []string{"materialTerms", "rules", "reminder", "version"}, version: true},
		{path: []string{"displayContent", "layout"}, strings: []string{"compact", "featured"}},
		{path: []string{"cta", "actionKind"}, strings: []string{"none", "openTrade"}},
		{path: []string{"materialTerms", "rewardDelivery"}, strings: []string{"display-only"}},
	}
	nodes := map[string]map[string]json.RawMessage{"": envelope}
	for _, field := range fields {
		raw, err := readContractField(envelope, field.path, nodes)
		if err != nil {
			return false, err
		}
		if field.version {
			var version int64
			if json.Unmarshal(raw, &version) != nil || version < 1 {
				return false, unavailable("stored contract discriminator invalid")
			}
			if version != 1 {
				return false, nil
			}
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || value == "" {
			return false, unavailable("stored contract discriminator invalid")
		}
		known := false
		for _, expected := range field.strings {
			if value == expected {
				known = true
				break
			}
		}
		if !known {
			return false, nil
		}
	}
	return true, nil
}
func readContractField(root map[string]json.RawMessage, path []string, nodes map[string]map[string]json.RawMessage) (json.RawMessage, error) {
	current := root
	prefix := ""
	for index, name := range path {
		raw, ok := current[name]
		if !ok || string(raw) == "null" {
			return nil, unavailable("stored contract discriminator missing")
		}
		if index == len(path)-1 {
			return raw, nil
		}
		prefix += "/" + name
		if cached, ok := nodes[prefix]; ok {
			current = cached
			continue
		}
		var next map[string]json.RawMessage
		if json.Unmarshal(raw, &next) != nil || next == nil {
			return nil, unavailable("stored contract discriminator shape invalid")
		}
		nodes[prefix] = next
		current = next
	}
	return nil, unavailable("stored contract discriminator path invalid")
}
