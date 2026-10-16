package web

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// mountLearn puts Audio Courses (github.com/anchoo2kewl/go-learn, running as its own service on this Mac)
// behind the Learn tab: /learn/ is proxied to it, tailnet-only like the rest of the hub's private routes.
// Learn sees the hub's loopback connection and treats it as the owner, so a client-sent
// Tailscale-User-Login header is dropped rather than passed on.
func (s *Server) mountLearn(mux *http.ServeMux) {
	if s.LearnURL == "" {
		return
	}
	target, err := url.Parse(s.LearnURL)
	if err != nil {
		s.Log.Printf("learn: bad -learn URL %q: %v", s.LearnURL, err)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path = "/" + trimLearn(r.In.URL.Path)
			r.Out.URL.RawPath = ""
			r.Out.Header.Del("Tailscale-User-Login")
			r.Out.Header.Del("Tailscale-User-Name")
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.Log.Printf("learn: %v", err)
			http.Error(w, "Learn isn't running on this Mac (launchctl print gui/$UID/com.go-learn.server)", http.StatusBadGateway)
		},
	}
	mux.HandleFunc("GET /learn", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/learn/", http.StatusFound) })
	mux.Handle("/learn/", s.tailnetOnly(proxy.ServeHTTP))
}

func trimLearn(p string) string {
	const prefix = "/learn/"
	if len(p) >= len(prefix) && p[:len(prefix)] == prefix {
		return p[len(prefix):]
	}
	return ""
}
