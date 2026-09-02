package website

import (
	"embed"
	"log"
	"net/http"

	"Multipacker/rss-reader/src/db"

	"github.com/klauspost/compress/gzhttp"
)



//go:embed all:static
var staticFiles embed.FS



func HandleIndex(templateExecutor TemplateExecutor) http.HandlerFunc {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/entries", http.StatusFound)
	})
}



func MiddlewareLogging(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func (w http.ResponseWriter, r *http.Request) {
		logger.Printf("\"%v %v %v\" \"%v\" %v\n", r.Method, r.URL.Path, r.Proto, r.UserAgent(), r.RemoteAddr)
		next.ServeHTTP(w, r)
	})
}



func NewWebsiteRoutes(reload bool, config Config, dbConnection db.Database) http.Handler {
	templateExecutor := NewTemplateExecutor(reload)

	mux := http.NewServeMux()
	mux.Handle("/", HandleIndex(templateExecutor))
	// NOTE(simon): Allow reloading static files.
	if reload {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("src/website/static"))))
	} else {
		mux.Handle("/static/", http.FileServerFS(staticFiles))
	}

	mux.Handle("GET /feeds", HandleFeedsGet(templateExecutor))
	mux.Handle("POST /feeds", HandleFeedsPost(templateExecutor, dbConnection))

	mux.Handle("GET /entries", HandleEntriesGet(templateExecutor))
	mux.Handle("POST /entries", HandleEntriesPost(templateExecutor, dbConnection))

	gzWrapper, err := gzhttp.NewWrapper()
	if err != nil {
		log.Fatalln(err)
	}

	return MiddlewareLogging(log.Default(), gzWrapper(mux))
}
