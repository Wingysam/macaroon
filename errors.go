package macaroon

import (
	"errors"
	"fmt"
)

var (
	ErrUnrecognizedToken          = errors.New("bad token")
	ErrUnauthorized               = errors.New("unauthorized")
	ErrInvalidAccess              = fmt.Errorf("%w: bad data for token verification", ErrUnauthorized)
	ErrResourcesMutuallyExclusive = fmt.Errorf("%w: resources are mutually exclusive", ErrInvalidAccess)
	ErrResourceUnspecified        = fmt.Errorf("%w: must specify", ErrInvalidAccess)
	ErrUnauthorizedForResource    = fmt.Errorf("%w for", ErrUnauthorized)
	ErrUnauthorizedForAction      = fmt.Errorf("%w for", ErrUnauthorized)
	ErrBadCaveat                  = fmt.Errorf("%w: bad caveat", ErrUnauthorized)
)
