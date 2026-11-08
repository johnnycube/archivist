package rest

import "time"

// SetLimiterClock lets the external tests drive the per-IP rate limiter's
// time, so a test of the login throttle is not cut short by the limiter.
func (s *Server) SetLimiterClock(now func() time.Time) { s.ipLimiter.now = now }
