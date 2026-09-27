package platform

import (
	"net"
	"net/http"
)

func (s *Service) clientIP(r *http.Request) string {
	if s.cfg.Auth != nil {
		return s.cfg.Auth.ClientIP(r)
	}
	h, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil {
		return h
	}
	return r.RemoteAddr
}

// HTTP bounds include anonymous discovery and replay; state/command windows have
// separate authority checks and are never prolonged by a network retry.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limit("http:"+s.clientIP(r), 1200) {
			failure(w, api(429, "RATE_LIMITED"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
