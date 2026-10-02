package model

import "errors"

var (
	errMissingFields = errors.New("trackingNumber, eventId, code and occurredAt are required")
	errBadStatus     = errors.New("unknown status value")
)
