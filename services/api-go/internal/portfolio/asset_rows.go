package portfolio

import (
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

// Amounts, notional exposure and project market caps are different quantities.
// None of these sources supplies a reliable personal USD valuation.
func assetRows(ownerAddress string, chainID *int, stakes []staking.UserStake, positions []trading.PerpPositionForAccount, createdTokens []token.Token, holdings []token.HoldingForOwner) []RawAsset {
	holderByTokenID := make(map[string]token.HoldingForOwner, len(holdings))
	for _, h := range holdings {
		holderByTokenID[h.TokenID] = h
	}

	out := make([]RawAsset, 0, len(stakes)+len(positions)+len(createdTokens)+len(holdings))

	for _, st := range stakes {
		if chainID != nil && st.PoolChainID != *chainID {
			continue
		}
		out = append(out, RawAsset{
			ID:              "stake:" + st.ID,
			AssetType:       "earn",
			Symbol:          st.PoolName,
			Name:            st.PoolName,
			ChainID:         st.PoolChainID,
			RawBalance:      st.Amount,
			BalanceUnit:     BalanceToken,
			ValuationStatus: ValuationUnavailable,
			ValuationReason: optAddr(ReasonMissingUSDPrice),
			Status:          stakeStatus(st),
			Source:          "Atlas earn",
			Address:         optAddr(st.PoolTokenAddr),
			WalletAddress:   &ownerAddress,
			UpdatedAt:       iso(st.StakedAt),
			Metadata:        &RawAssetMeta{Origin: "protocol-position"},
		})
	}

	for _, p := range positions {
		out = append(out, RawAsset{
			ID:              "perp:" + p.ID,
			AssetType:       "perp",
			Symbol:          p.Token,
			Name:            p.Token,
			ChainID:         p.ChainID,
			RawBalance:      p.Size,
			BalanceUnit:     BalanceUSD30,
			ValuationStatus: ValuationUnavailable,
			ValuationReason: optAddr(ReasonPositionEquityUnavailable),
			Status:          strings.ToLower(p.Status),
			Source:          perpSource(p.IsLong),
			Address:         nil,
			WalletAddress:   &ownerAddress,
			UpdatedAt:       iso(p.UpdatedAt),
			Metadata:        &RawAssetMeta{Origin: "protocol-position"},
		})
	}

	for _, t := range createdTokens {
		linked, hasHolder := holderByTokenID[t.ID]
		if hasHolder {
			delete(holderByTokenID, t.ID)
		}
		rawBalance := ""
		holderUpdated := ""
		if hasHolder {
			rawBalance = linked.Balance
			holderUpdated = iso(linked.HolderUpdated)
		}
		creator := t.CreatorAddress
		var marketCapPtr *float64
		if t.MarketCap != nil {
			mc := parseDecimalDisplay(t.MarketCap)
			marketCapPtr = &mc
		}
		out = append(out, RawAsset{
			ID:              "token:" + t.ID,
			AssetType:       "token",
			Symbol:          t.Symbol,
			Name:            t.Name,
			ChainID:         t.ChainID,
			RawBalance:      rawBalance,
			BalanceUnit:     BalanceBaseUnit,
			ValuationStatus: ValuationUnavailable,
			ValuationReason: optAddr(ReasonHolderValuationUnavailable),
			Status:          strings.ToLower(t.Status),
			Source:          createdTokenSource(hasHolder),
			Address:         t.Address,
			WalletAddress:   &ownerAddress,
			UpdatedAt:       maxIso(holderUpdated, iso(t.CreatedAt)),
			Metadata: &RawAssetMeta{
				Origin:         "created-by-owner",
				IsOfficial:     t.IsOfficial,
				CreatorAddress: &creator,
				Tags:           tagsAsStrings(t.Tags),
				MarketCap:      marketCapPtr,
			},
		})
	}

	// A created token absorbs its indexed holding so the same quantity is not
	// counted twice. This read model does not include ordinary ERC20 transfers.
	for _, h := range holderByTokenID {
		creator := h.TokenCreator
		var marketCapPtr *float64
		if h.TokenMarketCap != nil {
			mc := *h.TokenMarketCap
			marketCapPtr = &mc
		}
		out = append(out, RawAsset{
			ID:              "holding:" + h.TokenID + ":" + h.UserAddress,
			AssetType:       "token",
			Symbol:          h.TokenSymbol,
			Name:            h.TokenName,
			ChainID:         h.TokenChainID,
			RawBalance:      h.Balance,
			BalanceUnit:     BalanceBaseUnit,
			ValuationStatus: ValuationUnavailable,
			ValuationReason: optAddr(ReasonHolderValuationUnavailable),
			Status:          strings.ToLower(h.TokenStatus),
			Source:          discoveredSource(h.TokenIsOfficial),
			Address:         h.TokenAddress,
			WalletAddress:   &ownerAddress,
			UpdatedAt:       iso(h.HolderUpdated),
			Metadata: &RawAssetMeta{
				Origin:         "wallet-discovered",
				IsOfficial:     h.TokenIsOfficial,
				CreatorAddress: &creator,
				Tags:           tagsAsStrings(h.TokenTags),
				MarketCap:      marketCapPtr,
			},
		})
	}

	return out
}

func stakeStatus(s staking.UserStake) string {
	if s.UnstakedAt != nil {
		return "closed"
	}
	return "active"
}

func perpSource(isLong bool) string {
	if isLong {
		return "Perp long"
	}
	return "Perp short"
}

func createdTokenSource(hasHolder bool) string {
	if hasHolder {
		return "Created asset · wallet holding"
	}
	return "Created asset"
}

func discoveredSource(isOfficial bool) string {
	if isOfficial {
		return "Wallet token"
	}
	return "Discovered token"
}

func iso(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func maxIso(a, b string) string {
	if compareIsoTimes(a, b) >= 0 {
		return a
	}
	return b
}

func compareIsoTimes(a, b string) int {
	// RFC3339Nano omits trailing fractional zeros, so lexical order is not time order.
	left, leftErr := time.Parse(time.RFC3339Nano, a)
	right, rightErr := time.Parse(time.RFC3339Nano, b)
	switch {
	case leftErr == nil && rightErr == nil:
		return left.Compare(right)
	case leftErr == nil:
		return 1
	case rightErr == nil:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

func optAddr(addr string) *string {
	if addr == "" {
		return nil
	}
	v := addr
	return &v
}

func tagsAsStrings(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}
