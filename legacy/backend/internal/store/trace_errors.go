package store

import "errors"

var (
	errNoTraceID = errors.New("store: trace needs an id")
	errNoSpanID  = errors.New("store: span needs trace and span ids")
)
