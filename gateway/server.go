package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/gorilla/csrf"
	"github.com/gorilla/mux"
	"github.com/phogolabs/parcello"
	"github.com/sahib/brig/catfs"
	"github.com/sahib/brig/events"
	"github.com/sahib/brig/gateway/db"
	"github.com/sahib/brig/gateway/endpoints"
	"github.com/sahib/brig/gateway/remotesapi"
	"github.com/sahib/config"
	log "github.com/sirupsen/logrus"
	"github.com/ulule/limiter"
	"github.com/ulule/limiter/drivers/middleware/stdlib"
	"github.com/ulule/limiter/drivers/store/memory"

	// Include static resources:
	_ "github.com/sahib/brig/gateway/static"
)

// set a max. amount of requests per hour:
var rate = limiter.Rate{
	Period: 1 * time.Hour,
	Limit:  50000,
}

// Gateway is a small HTTP server that is able to serve
// files from brig over HTTP. This can be used to share files
// inside of brig with users that do not use brig.
type Gateway struct {
	cfg         *config.Config
	isClosed    bool
	isReloading bool
	state       *endpoints.State
	evHdl       *endpoints.EventsHandler

	srv *http.Server
}

// NewGateway returns a newly built gateway.
// This function does not yet start a server.
func NewGateway(fs *catfs.FS, rapi remotesapi.RemotesAPI, cfg *config.Config, ev *events.Listener, dbPath string) (*Gateway, error) {
	userDb, err := db.NewUserDatabase(dbPath)
	if err != nil {
		return nil, err
	}

	evHdl := endpoints.NewEventsHandler(rapi, ev)
	state, err := endpoints.NewState(fs, rapi, cfg, evHdl, ev, userDb)
	if err != nil {
		return nil, err
	}

	gw := &Gateway{
		state:    state,
		isClosed: true,
		cfg:      cfg,
		evHdl:    evHdl,
	}

	// Restarts the gateway on the next possible idle phase:
	reloader := func(key string) {
		// Forbid recursive reloading.
		if gw.isReloading {
			return
		}

		gw.isReloading = true
		defer func() { gw.isReloading = false }()

		log.Debugf("reloading gateway because config key changed: %s", key)
		if err := gw.Stop(); err != nil {
			log.Errorf("failed to stop gateway: %v", err)
			return
		}

		gw.Start()
	}

	// If any of those vars change, we should reload.
	// All other config values are read on-demand anyways.
	cfg.AddEvent("enabled", reloader)
	cfg.AddEvent("port", reloader)
	cfg.AddEvent("auth.session-encryption-key", reloader)
	cfg.AddEvent("auth.session-authentication-key", reloader)
	cfg.AddEvent("auth.session-csrf-key", reloader)
	return gw, nil
}

// Stop stops the gateway gracefully.
func (gw *Gateway) Stop() error {
	if gw.isClosed {
		return nil
	}

	gw.isClosed = true
	if err := gw.state.Close(); err != nil {
		log.Warningf("failed to shutdown state object: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if gw.srv != nil {
		return gw.srv.Shutdown(ctx)
	}

	return nil
}

type csrfErrorHandler struct{}

func (ch *csrfErrorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Warningf("csrf failed: %v", r.Context().Value("gorilla.csrf.Error"))
	w.WriteHeader(http.StatusForbidden)
}

// Start will start the gateway.
// If the gateway is not enabled in the config, this does nothing.
// The gateway is started in the background, this method does not block.
func (gw *Gateway) Start() {
	if !gw.cfg.Bool("enabled") {
		log.Debugf("gateway is disabled in the config; doing nothing until enabled.")
		return
	}

	gw.isClosed = false

	port := gw.cfg.Int("port")
	addr := fmt.Sprintf(":%d", port)
	log.Debugf("starting gateway on %s", addr)

	gw.isReloading = true
	defer func() {
		gw.isReloading = false
	}()

	uiEnabled := gw.cfg.Bool("ui.enabled")

	// Use csrf protection for all routes by default.
	// This does not influence GET routes, only POST ones:
	router := mux.NewRouter()
	router.Use(endpoints.SecureMiddleware(gw.state))
	needsAuth := endpoints.AuthMiddleware(gw.state)

	csrfOpts := []csrf.Option{
		csrf.ErrorHandler(&csrfErrorHandler{}),
	}

	// TODO: We don't use HTTPS but recommend to use something like caddy.
	csrfOpts = append(csrfOpts, csrf.Secure(false))

	if uiEnabled {
		csrfKey := []byte(gw.cfg.String("auth.session-csrf-key"))
		router.Use(csrf.Protect(csrfKey, csrfOpts...))

		// API route definition:
		apiRouter := router.PathPrefix("/api/v0").Methods("POST").Subrouter()
		apiRouter.Handle("/login", endpoints.NewLoginHandler(gw.state))
		apiRouter.Handle("/whoami", endpoints.NewWhoamiHandler(gw.state))
		apiRouter.Handle("/ping", endpoints.NewPingHandler(gw.state))
		apiRouter.Handle("/logout", needsAuth(endpoints.NewLogoutHandler(gw.state)))
		apiRouter.Handle("/ls", needsAuth(endpoints.NewLsHandler(gw.state)))
		apiRouter.Handle("/upload", needsAuth(endpoints.NewUploadHandler(gw.state)))
		apiRouter.Handle("/move", needsAuth(endpoints.NewMoveHandler(gw.state)))
		apiRouter.Handle("/mkdir", needsAuth(endpoints.NewMkdirHandler(gw.state)))
		apiRouter.Handle("/copy", needsAuth(endpoints.NewCopyHandler(gw.state)))
		apiRouter.Handle("/remove", needsAuth(endpoints.NewRemoveHandler(gw.state)))
		apiRouter.Handle("/history", needsAuth(endpoints.NewHistoryHandler(gw.state)))
		apiRouter.Handle("/reset", needsAuth(endpoints.NewResetHandler(gw.state)))
		apiRouter.Handle("/all-dirs", needsAuth(endpoints.NewAllDirsHandler(gw.state)))
		apiRouter.Handle("/log", needsAuth(endpoints.NewLogHandler(gw.state)))
		apiRouter.Handle("/deleted", needsAuth(endpoints.NewDeletedPathsHandler(gw.state)))
		apiRouter.Handle("/undelete", needsAuth(endpoints.NewUndeleteHandler(gw.state)))
		apiRouter.Handle("/pin", needsAuth(endpoints.NewPinHandler(gw.state)))
		apiRouter.Handle("/unpin", needsAuth(endpoints.NewUnpinHandler(gw.state)))

		// Remote API:
		apiRouter.Handle("/remotes/list", needsAuth(endpoints.NewRemotesListHandler(gw.state)))
		apiRouter.Handle("/remotes/add", needsAuth(endpoints.NewRemotesAddHandler(gw.state)))
		apiRouter.Handle("/remotes/modify", needsAuth(endpoints.NewRemotesModifyHandler(gw.state)))
		apiRouter.Handle("/remotes/remove", needsAuth(endpoints.NewRemotesRemoveHandler(gw.state)))
		apiRouter.Handle("/remotes/self", needsAuth(endpoints.NewRemotesSelfHandler(gw.state)))
		apiRouter.Handle("/remotes/sync", needsAuth(endpoints.NewRemotesSyncHandler(gw.state)))
		apiRouter.Handle("/remotes/diff", needsAuth(endpoints.NewRemotesDiffHandler(gw.state)))
	}

	// Add the /get endpoint. Since it might contain any path, we have to
	// Use a path prefix so the right handler is called.
	// NOTE: /get does its own auth handling currently,
	// since it needs to be available if somebody is not using the UI.
	router.PathPrefix("/get").Handler(endpoints.NewGetHandler(gw.state)).Methods("GET")

	if uiEnabled {
		// /events is a websocket that pushes events to the client.
		// The client will probably call /ls then.
		router.PathPrefix("/events").Handler(needsAuth(gw.evHdl)).Methods("GET")

		// Special case: index.html gets a csrf token:
		idxHdl := endpoints.NewIndexHandler(gw.state)
		router.Handle("/", idxHdl).Methods("GET")
		router.Handle("/index.html", idxHdl).Methods("GET")

		spaRoutes := []string{
			"/view",
			"/log",
			"/remotes",
			"/deleted",
			"/diff",
			"/nothing",
		}

		for _, route := range spaRoutes {
			router.PathPrefix(route).Handler(idxHdl).Methods("GET")
		}

		// Serve all files in the static directory as-is.
		// This has to come last, since it's a wildcard for everything else.
		// The static files are packed inside the binary (for now)

		if gw.cfg.Bool("ui.debug_mode") {
			router.PathPrefix("/").Handler(http.FileServer(http.Dir("./gateway/static")))
		} else {
			router.PathPrefix("/").Handler(http.FileServer(parcello.ManagerAt("/")))
		}
	}

	// Implement rate limiting:
	router.Use(
		stdlib.NewMiddleware(
			limiter.New(memory.NewStore(), rate),
			stdlib.WithForwardHeader(true),
		).Handler,
	)

	gw.srv = &http.Server{
		Addr:              addr,
		Handler:           gziphandler.GzipHandler(router),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       360 * time.Second,
		// We cant' really enable write timeout, since upload will break then.
		// See also: https://github.com/golang/go/issues/16100
		// WriteTimeout:      10 * time.Second,
	}

	go func() {
		err := gw.srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			log.Errorf("serve failed: %v", err)
		}
	}()
}

// UserDatabase returns the user database API.
func (gw *Gateway) UserDatabase() *db.UserDatabase {
	return gw.state.UserDatabase()
}

// Close the gateway and clean up all open resouces.
func (gw *Gateway) Close() error {
	return gw.state.UserDatabase().Close()
}
