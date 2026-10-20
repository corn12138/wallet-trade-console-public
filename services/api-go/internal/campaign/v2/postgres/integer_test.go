package postgres

import (
	"math"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestUnsignedCounterPostgresEncodingCannotWrap(t *testing.T) {
	codec := pgtype.NewMap()
	if got, err := codec.Encode(pgtype.Int8OID, pgtype.TextFormatCode, integer(nil), nil); err != nil || got != nil {
		t.Fatalf("null counter: %q %v", got, err)
	}
	for _, value := range []uint64{0, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		got, err := codec.Encode(pgtype.Int8OID, pgtype.TextFormatCode, integer(&value), nil)
		if value <= math.MaxInt64 {
			if err != nil || string(got) != strconv.FormatUint(value, 10) {
				t.Fatalf("counter %d: %q %v", value, got, err)
			}
		} else if err == nil {
			t.Fatalf("counter %d encoded as %q", value, got)
		}
	}
}
