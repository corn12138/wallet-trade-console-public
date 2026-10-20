package txreview

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const reviewBodyLimit = 64 * 1024
const reviewTimeout = 10 * time.Second

// Unknown execution extensions cannot be discarded while claiming that the
// submitted transaction was simulated in full. Generated TxShape values retain
// the same four-field JSON contract used by existing consumers.
func (tx *TxShape) UnmarshalJSON(raw []byte) error {
	type plain TxShape
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: unsupported transaction field or shape", ErrInvalidInput)
	}
	*tx = TxShape(value)
	return nil
}

func validateCalldata(value string) error {
	if !strings.HasPrefix(value, "0x") || len(value) > 2+16*1024*2 {
		return fmt.Errorf("%w: tx.data must be hex calldata of at most 16 KiB", ErrInvalidInput)
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return fmt.Errorf("%w: tx.data must contain complete hex bytes", ErrInvalidInput)
	}
	return nil
}

func decodeReviewInput(w http.ResponseWriter, r *http.Request) (ReviewInput, bool) {
	var input ReviewInput
	// The body deadline applies before decoding, so slow request bodies consume
	// the same budget as RPC work instead of receiving a fresh ten seconds.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(reviewTimeout))
	defer func() { _ = http.NewResponseController(w).SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, reviewBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	if err == nil {
		var trailing any
		err = decoder.Decode(&trailing)
		if err == io.EOF {
			return input, true
		}
		if err == nil {
			err = errors.New("multiple JSON values")
		}
	}
	var tooLarge *http.MaxBytesError
	var timedOut net.Error
	switch {
	case errors.As(err, &tooLarge):
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request body exceeds 64 KiB")
	case errors.Is(r.Context().Err(), context.DeadlineExceeded) || (errors.As(err, &timedOut) && timedOut.Timeout()):
		writeJSONError(w, http.StatusRequestTimeout, "transaction review timed out")
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body or unsupported field")
	}
	return ReviewInput{}, false
}
