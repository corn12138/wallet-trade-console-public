package auth

import (
	"context"
	"testing"
)

func TestWithAddress(t *testing.T) {
	// Bypass middleware for unit-testing downstream handlers.
	ctx := WithAddress(context.Background(), "0xABCD000000000000000000000000000000000000")
	if got := AddressFromContext(ctx); got != "0xabcd000000000000000000000000000000000000" {
		t.Errorf("got %q, want lowercase 0xabcd...", got)
	}
}

func TestResolveOwner(t *testing.T) {
	cases := []struct {
		name      string
		auth      string
		requested string
		want      string
		wantErr   error
	}{
		{
			name: "no requested returns authenticated",
			auth: "0xABCD000000000000000000000000000000000000",
			want: "0xabcd000000000000000000000000000000000000",
		},
		{
			name:      "matching requested (different case)",
			auth:      "0xabcd000000000000000000000000000000000000",
			requested: "0xABCD000000000000000000000000000000000000",
			want:      "0xabcd000000000000000000000000000000000000",
		},
		{
			name:      "mismatched requested is forbidden",
			auth:      "0xabcd000000000000000000000000000000000000",
			requested: "0x000000000000000000000000000000000000DEAD",
			wantErr:   ErrOwnerMismatch,
		},
		{
			name:      "invalid requested is bad-request",
			auth:      "0xabcd000000000000000000000000000000000000",
			requested: "not-an-address",
			wantErr:   ErrInvalidRequestedAddress,
		},
		{
			name:    "missing authenticated is unauthorized",
			auth:    "",
			wantErr: ErrMissingAuthenticated,
		},
		{
			name:    "non-address authenticated is unauthorized",
			auth:    "not-an-address",
			wantErr: ErrMissingAuthenticated,
		},
	}
	for _, tc := range cases {
		got, err := ResolveOwner(tc.auth, tc.requested)
		if tc.wantErr != nil {
			if err != tc.wantErr {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v, want nil", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
