package httpapi

import "errors"

// errInvalidSession marks a session that exists but is unusable
// (expired, MFA-pending, or the user was deactivated).
var errInvalidSession = errors.New("invalid session")

// errMissingCarrierSig marks webhooks without a signature header.
var errMissingCarrierSig = errors.New("missing signature header")

// errBadCarrierSig marks webhooks whose HMAC does not verify.
var errBadCarrierSig = errors.New("signature verification failed")