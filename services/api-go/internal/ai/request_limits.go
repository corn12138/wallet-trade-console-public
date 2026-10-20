package ai

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

func decodeExplainRequest(w http.ResponseWriter, r *http.Request) (ExplainRequest, bool) {
	var req ExplainRequest
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = http.NewResponseController(w).SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&req)
	if err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if err == io.EOF {
			return req, true
		}
	}
	var tooLarge *http.MaxBytesError
	var timedOut net.Error
	switch {
	case errors.As(err, &tooLarge):
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request body exceeds 64 KiB")
	case errors.As(err, &timedOut) && timedOut.Timeout():
		writeJSONError(w, http.StatusRequestTimeout, "explanation request timed out")
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body or unsupported field")
	}
	return ExplainRequest{}, false
}
