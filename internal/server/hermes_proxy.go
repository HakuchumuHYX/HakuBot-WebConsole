package server

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

const hermesPrefix = "/hermes"

func newHermesProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = nil
	proxy.Rewrite = func(request *httputil.ProxyRequest) {
		path := strings.TrimPrefix(request.In.URL.Path, hermesPrefix)
		if path == "" {
			path = "/"
		}
		request.Out.URL.Scheme = target.Scheme
		request.Out.URL.Host = target.Host
		request.Out.URL.Path = path
		request.Out.URL.RawPath = ""
		request.Out.Host = request.In.Host
		if forwardedFor := request.In.Header.Values("X-Forwarded-For"); len(forwardedFor) > 0 {
			request.Out.Header["X-Forwarded-For"] = append(
				[]string(nil),
				forwardedFor...,
			)
		}
		request.Out.Header.Set("X-Forwarded-Prefix", hermesPrefix)
		request.Out.Header.Set("X-Forwarded-Host", request.In.Host)
		proto := request.In.Header.Get("X-Forwarded-Proto")
		if proto != "https" && proto != "http" {
			proto = "http"
		}
		request.Out.Header.Set("X-Forwarded-Proto", proto)
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, _ error) {
		writeError(writer, http.StatusServiceUnavailable, "Hermes service unavailable")
	}
	return proxy
}
