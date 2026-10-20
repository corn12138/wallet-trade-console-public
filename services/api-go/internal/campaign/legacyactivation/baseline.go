package legacyactivation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
)

// campaignColumns are the launchpad_campaigns columns the baseline keeps. A
// row with any other column cannot be represented and is refused.
var campaignColumns = []string{"id", "title", "description", "banner", "start_date", "end_date", "status", "reward", "participants", "metadata", "created_at", "updated_at"}

var lowerWallet = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// legacyTimestamp turns a TIMESTAMP(3) value as row_to_json renders it into
// the baseline's millisecond UTC form (the Prisma UTC convention the
// inventory documents).
func legacyTimestamp(raw json.RawMessage) (time.Time, string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return time.Time{}, "", err
	}
	value, err := time.Parse("2006-01-02T15:04:05.999999999", text)
	if err != nil {
		return time.Time{}, "", err
	}
	if value.Nanosecond()%int(time.Millisecond) != 0 {
		return time.Time{}, "", fmt.Errorf("timestamp %q is finer than milliseconds", text)
	}
	formatted := v2.FormatLegacyTime(value)
	if _, err = v2.ParseLegacyTime(formatted); err != nil {
		return time.Time{}, "", err
	}
	return value.UTC(), formatted, nil
}

func nullableText(raw json.RawMessage) (*string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, err
	}
	return &text, nil
}

// baselineFromArchive builds the baseline revision input from the archived
// campaign row, verbatim, and returns the stored legacy status.
func baselineFromArchive(archived legacyimport.ArchivedCampaign) (v2.LegacyBaselineInput, error) {
	var input v2.LegacyBaselineInput
	var row map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(archived.Campaign.JSON))
	decoder.UseNumber()
	if err := decoder.Decode(&row); err != nil {
		return input, err
	}
	if len(row) != len(campaignColumns) {
		return input, fmt.Errorf("the legacy row has %d columns; the baseline keeps exactly %d", len(row), len(campaignColumns))
	}
	for _, column := range campaignColumns {
		if _, ok := row[column]; !ok {
			return input, fmt.Errorf("the legacy row has no %s column", column)
		}
	}
	var id string
	if err := json.Unmarshal(row["id"], &id); err != nil || id != archived.CampaignID {
		return input, fmt.Errorf("the archived row is not campaign %s", archived.CampaignID)
	}
	terms, record := &input.MaterialTerms.Legacy, &input.LegacyRecord
	var err error
	if err = json.Unmarshal(row["title"], &terms.Title); err != nil {
		return input, fmt.Errorf("title: %w", err)
	}
	if terms.Description, err = nullableText(row["description"]); err != nil {
		return input, fmt.Errorf("description: %w", err)
	}
	if terms.Reward, err = nullableText(row["reward"]); err != nil {
		return input, fmt.Errorf("reward: %w", err)
	}
	if record.Banner, err = nullableText(row["banner"]); err != nil {
		return input, fmt.Errorf("banner: %w", err)
	}
	if err = json.Unmarshal(row["status"], &record.Status); err != nil {
		return input, fmt.Errorf("status: %w", err)
	}
	if err = json.Unmarshal(row["participants"], &record.Participants); err != nil {
		return input, fmt.Errorf("participants: %w", err)
	}
	if !bytes.Equal(bytes.TrimSpace(row["metadata"]), []byte("null")) {
		metadata := string(row["metadata"])
		record.MetadataJSON = &metadata
	}
	for _, field := range []struct {
		column string
		target *string
	}{{"start_date", &terms.StartAt}, {"end_date", &terms.EndAt}, {"created_at", &record.CreatedAt}, {"updated_at", &record.UpdatedAt}} {
		if _, *field.target, err = legacyTimestamp(row[field.column]); err != nil {
			return input, fmt.Errorf("%s: %w", field.column, err)
		}
	}
	input.SchemaVersion, input.TermsCanonicalVersion, input.TemplateID = 1, 1, v2.LegacyBaselineTemplate
	input.Source = v2.LegacySource{RunID: archived.RunID, CapturedAt: archived.CapturedAt.UTC().Format(time.RFC3339Nano), Algorithm: legacyimport.DigestAlgorithm,
		LegacyRowSHA256: archived.Item.LegacyRowSHA256, InteractionsSHA256: archived.Item.InteractionsSHA256}
	if err = input.Validate(); err != nil {
		return input, err
	}
	return input, nil
}

// interactionsFromArchive merges the archived participant and reminder rows
// into one state per lowercase wallet and kind, keeping the earliest legacy
// time and every source row.
func interactionsFromArchive(archived legacyimport.ArchivedCampaign) ([]postgres.LegacyInteraction, error) {
	merged := map[string]*postgres.LegacyInteraction{}
	for _, group := range []struct {
		kind, timeColumn string
		rows             []legacyimport.ArchivedRow
	}{{"join", "joined_at", archived.Participants}, {"reminder", "created_at", archived.Reminders}} {
		for _, row := range group.rows {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(row.JSON), &fields); err != nil {
				return nil, err
			}
			var campaignID, wallet string
			if json.Unmarshal(fields["campaign_id"], &campaignID) != nil || campaignID != archived.CampaignID {
				return nil, fmt.Errorf("archived %s row is not for campaign %s", group.kind, archived.CampaignID)
			}
			if json.Unmarshal(fields["wallet_address"], &wallet) != nil {
				return nil, fmt.Errorf("archived %s row has no wallet", group.kind)
			}
			wallet = strings.ToLower(wallet)
			if !lowerWallet.MatchString(wallet) || strings.Trim(wallet[2:], "0") == "" {
				return nil, fmt.Errorf("archived %s row has an invalid wallet", group.kind)
			}
			at, _, err := legacyTimestamp(fields[group.timeColumn])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", group.timeColumn, err)
			}
			key := wallet + "\x00" + group.kind
			item, ok := merged[key]
			if !ok {
				item = &postgres.LegacyInteraction{Wallet: wallet, Kind: group.kind, AcceptedAt: at}
				merged[key] = item
			} else if at.Before(item.AcceptedAt) {
				item.AcceptedAt = at
			}
			item.SourceRows = append(item.SourceRows, row.SHA256)
		}
	}
	out := make([]postgres.LegacyInteraction, 0, len(merged))
	for _, item := range merged {
		sort.Strings(item.SourceRows)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Wallet+out[i].Kind < out[j].Wallet+out[j].Kind })
	return out, nil
}
