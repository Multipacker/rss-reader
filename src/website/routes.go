package website

import (
	"embed"
	"html/template"
	"log"
	"net/http"

	"github.com/klauspost/compress/gzhttp"
)



//go:embed all:static
var staticFiles embed.FS



func HandleIndex(templateExecutor TemplateExecutor, storage *Storage) http.HandlerFunc {
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



func NewWebsiteRoutes(reload bool, config Config, storage *Storage) http.Handler {
	// NOTE(simon): Setup handler for reloading of static files
	var staticHandler http.Handler
	var templateExecutor TemplateExecutor
	if reload {
		staticHandler = http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
		templateExecutor = DebugTemplateExecutor{"templates/*.gohtml"}
	} else {
		staticHandler = http.FileServerFS(staticFiles)
		templateExecutor = template.Must(template.ParseFS(templateFiles, "**/*.gohtml"))
	}

	mux := http.NewServeMux()
	mux.Handle("/", HandleIndex(templateExecutor, storage))
	mux.Handle("/static/", staticHandler)
	mux.Handle("GET /feeds", HandleFeedsGet(templateExecutor, storage))
	mux.Handle("POST /feeds", HandleFeedsPost(templateExecutor, storage))
	mux.Handle("GET /entries", HandleEntriesGet(templateExecutor, storage))
	mux.Handle("POST /entries", HandleEntriesPost(templateExecutor, storage))

	gzWrapper, err := gzhttp.NewWrapper()
	if err != nil {
		log.Fatalln(err)
	}

	return MiddlewareLogging(log.Default(), gzWrapper(mux))
}
