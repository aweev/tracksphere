package httpapi

import "time"

// timeNowUTC is a seam for tests; production uses the wall clock.
var timeNowUTC = func() time.Time { return time.Now().UTC() }