package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/docshelf/docshelf"
	"github.com/go-chi/chi"
	"github.com/go-chi/cors"
	"github.com/sirupsen/logrus"
)

// A Server is a collection of stores that get wired up to HTTP endpoint.
type Server struct {
	host           string
	port           uint
	log            *logrus.Logger
	authenticators map[string]docshelf.Authenticator

	DocHandler  DocHandler
	UserStore   docshelf.UserStore
	GroupStore  docshelf.GroupStore
	PolicyStore docshelf.PolicyStore
}

// NewServer returns a new Server struct.
func NewServer(host string, port uint, logger *logrus.Logger) Server {
	return Server{
		host:           host,
		port:           port,
		log:            logger,
		authenticators: make(map[string]docshelf.Authenticator),
	}
}

// AddAuth method to server.
func (s Server) AddAuth(name string, auth docshelf.Authenticator) {
	s.authenticators[name] = auth
}

// Start fires up an HTTP server and listens for incoming requests.
func (s Server) Start() error {
	s.log.WithField("host", s.host).WithField("port", s.port).Info("server starting")
	// if err := s.CheckStores(); err != nil {
	// 	return err
	// }

	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", s.host, s.port), s.buildRoutes()); err != nil {
		return err
	}

	return nil
}

// CheckHandlers returns an error if the Server contains any invalid handlers.
func (s Server) CheckHandlers() error {
	if s.UserStore == nil {
		return errors.New("no UserStore set")
	}

	if s.GroupStore == nil {
		return errors.New("no GroupStore set")
	}

	if s.PolicyStore == nil {
		return errors.New("no PolicyStore set")
	}

	if len(s.authenticators) == 0 {
		return errors.New("no Authenticator set")
	}

	return nil
}

func (s Server) buildRoutes() chi.Router {
	router := chi.NewRouter()
	cors := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	})

	userHandler := NewUserHandler(s.UserStore, s.log)
	router.Use(cors.Handler)
	router.Route("/api", func(r chi.Router) {
		r.Use(Authentication(s.UserStore))
		r.Route("/user", func(r chi.Router) {
			r.Get("/", userHandler.GetCurrentUser)
			r.Get("/list", userHandler.GetUsers)
			r.Post("/", userHandler.PostUser)
			r.Get("/{id}", userHandler.GetUser)
			r.Delete("/{id}", userHandler.DeleteUser)
		})

		r.Route("/doc", func(r chi.Router) {
			r.Post("/", s.DocHandler.PostDoc)
			r.Get("/list", s.DocHandler.GetList)
			r.Post("/{id}/pin", s.DocHandler.PinDoc)
			r.Post("/{id}/tag", s.DocHandler.PostTag)
			r.Get("/{id}", s.DocHandler.GetDoc)
			r.Delete("/{id}", s.DocHandler.DeleteDoc)
		})
	})

	router.Get("/doc/{path}", s.DocHandler.RenderDoc)
	router.Post("/login", s.handleLogin)
	router.Get("/logout", handleLogout)
	router.Get("/oauth/{provider}", s.handleOauth)

	// router.Handle("/*", http.FileServer(http.Dir("./ui/dist/")))
	router.Handle("/*", http.HandlerFunc(s.handleDefault))

	return router
}

func (s Server) handleDefault(w http.ResponseWriter, r *http.Request) {
	s.log.WithField("url", r.URL.String()).Info("handling unkown request")
}

func (s Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var login docshelf.User
	s.log.Info("handling login")
	if err := json.NewDecoder(r.Body).Decode(&login); err != nil {
		s.log.Error(err)
		badRequest(w, "invalid authentication data")
		return
	}

	provider := "basic"
	if login.Email == "" {
		provider = "google"
	}

	user, err := s.authenticators[provider].Authenticate(r.Context(), login.Email, login.Token)
	if err != nil {
		s.log.Error(err)
		unauthorized(w, "invalid credentials")
		return
	}

	// TODO (erik): This is a hack to make it easy to have "auth" during dev. This is *NOT* secure, by any means :D
	identity := http.Cookie{
		Name:     "session",
		Value:    user.ID,
		Path:     "/",
		HttpOnly: true,
	}

	http.SetCookie(w, &identity)
	noContent(w)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {

	// TODO (erik): This is a hack to make it easy to have "auth" during dev. This is *NOT* secure, by any means :D
	identity := http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
	}

	http.SetCookie(w, &identity)
	// need to force a refresh so the app figures the user is invalid
	redirect(w, "http://localhost:9001")
}

func (s Server) handleOauth(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	code := r.URL.Query().Get("code")
	s.log.WithField("provider", provider).Info("handling oauth")
	user, err := s.authenticators[provider].Authenticate(r.Context(), "", code)
	if err != nil {
		s.log.WithError(err).WithField("provider", provider).Error("failed to authenticate with provider")
		serverError(w, "authentication failed")
		return
	}

	identity := http.Cookie{
		Name:     "session",
		Value:    user.ID,
		Path:     "/",
		HttpOnly: true,
	}

	http.SetCookie(w, &identity)
	redirect(w, "http://localhost:9001")
}

// everything down here is setup for attaching certain data to the request context.
type contextKey string

const userKey = contextKey("ds-user")

func getContextUser(ctx context.Context) (docshelf.User, error) {
	if user, ok := ctx.Value(userKey).(docshelf.User); ok {
		return user, nil
	}

	return docshelf.User{}, errors.New("no user found in context")
}
