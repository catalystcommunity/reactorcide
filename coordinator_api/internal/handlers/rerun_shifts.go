package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// maxShiftBodyBytes bounds the body of a retry or re-run request.
const maxShiftBodyBytes = 16 << 10

// readShiftsBody reads the optional {"shifts": {...}} body of a REST retry or
// re-run. It writes the error response and returns ok=false for a body that
// asks for any other change (D25) or has an invalid shift.
func readShiftsBody(h *BaseHandler, w http.ResponseWriter, r *http.Request) (models.RunShifts, bool) {
	if r.Body == nil {
		return models.RunShifts{}, true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxShiftBodyBytes+1))
	if err != nil || len(body) > maxShiftBodyBytes {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "the request body is too large or cannot be read"})
		return models.RunShifts{}, false
	}
	shifts, err := runshifts.ParseRequestBody(body)
	if err != nil {
		respondShiftError(h, w, err)
		return models.RunShifts{}, false
	}
	return shifts, true
}

// respondShiftError writes a 400 for a refused or invalid shift.
func respondShiftError(h *BaseHandler, w http.ResponseWriter, err error) {
	code := "invalid_shift"
	switch {
	case errors.Is(err, runshifts.ErrRefused):
		code = "rerun_change_refused"
	case errors.Is(err, runshifts.ErrOverLimit):
		code = "shift_over_limit"
	}
	h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: code, Message: err.Error()})
}
