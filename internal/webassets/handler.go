package webassets

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist
var embedded embed.FS

func New(api http.Handler) http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure(w)
		if isAPI(r.URL.Path) {
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "." {
			if info, statErr := fs.Stat(dist, name); statErr == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		files.ServeHTTP(w, r)
	})
}

func isAPI(requestPath string) bool {
	return requestPath == "/v1" || requestPath == "/v2" || strings.HasPrefix(requestPath, "/v1/") || strings.HasPrefix(requestPath, "/v2/")
}

func secure(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}
