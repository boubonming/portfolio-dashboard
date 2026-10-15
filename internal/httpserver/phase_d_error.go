package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync/atomic"
)

type phaseDErrorBody struct {
	Error phaseDError `json:"error"`
}

type phaseDError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

var phaseDRequestSequence uint64

// writePhaseDError is the shared error writer for machine-capable Phase D
// endpoints. The request ID is generated locally and never contains request
// data, credentials, or database values.
func writePhaseDError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, phaseDErrorBody{Error: phaseDError{Code: code, Message: message, RequestID: phaseDRequestID()}})
}

func phaseDRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		value := atomic.AddUint64(&phaseDRequestSequence, 1)
		return fmt.Sprintf("req-%016x", value)
	}
	return "req-" + hex.EncodeToString(bytes[:])
}
