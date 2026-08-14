package trading

import (
	"strings"
	"testing"
)

// Fake paper rows must not feed the live orderbook / pending-order views:
// with paper disabled (the default) both queries pin "txHash" IS NOT NULL;
// the explicit dev opt-in removes the pin.
func TestOrderQueries_ExcludePaperRowsByDefault(t *testing.T) {
	chain := 11155111

	book, _ := buildOrderbookOrdersQuery("ETH-USD", &chain, false)
	if !strings.Contains(book, `"txHash" IS NOT NULL`) {
		t.Errorf("orderbook query missing paper exclusion: %s", book)
	}
	pending, _ := buildPendingOrdersQuery("0xAbC", "", &chain, 50, false)
	if !strings.Contains(pending, `"txHash" IS NOT NULL`) {
		t.Errorf("pending query missing paper exclusion: %s", pending)
	}
}

func TestOrderQueries_DevOptInIncludesPaperRows(t *testing.T) {
	book, _ := buildOrderbookOrdersQuery("ETH-USD", nil, true)
	if strings.Contains(book, `"txHash" IS NOT NULL`) {
		t.Errorf("opt-in orderbook still excludes paper rows: %s", book)
	}
	pending, _ := buildPendingOrdersQuery("0xAbC", "ETH-USD", nil, 10, true)
	if strings.Contains(pending, `"txHash" IS NOT NULL`) {
		t.Errorf("opt-in pending still excludes paper rows: %s", pending)
	}
}
