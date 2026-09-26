package apidocs

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/yuebanhome/openmajiang/api"
)

// RegisterRoutes publishes immutable client documentation, never runtime state.
func RegisterRoutes(mux *http.ServeMux) {
	for _, name := range []string{"openapi.json", "llms.txt", "llms-full.txt"} {
		path := name
		mux.HandleFunc("GET /"+path, func(w http.ResponseWriter, r *http.Request) { serve(w, r, path) })
	}
	mux.HandleFunc("GET /schemas/{name}", func(w http.ResponseWriter, r *http.Request) {
		switch name := r.PathValue("name"); name {
		case "dto.json", "protocol.json", "ws-client.json", "ws-server.json", "spectator.json":
			serve(w, r, "schemas/"+name)
		default:
			http.NotFound(w, r)
		}
	})
}

func serve(w http.ResponseWriter, r *http.Request, path string) {
	b, err := api.Files.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=300")
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(b))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if strings.HasSuffix(path, ".json") {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(b)
}
