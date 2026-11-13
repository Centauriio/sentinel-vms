// Package server is Sentinel's management side (PLATFORM.md, "Components
// and ownership"): users and sessions, the camera configuration (source of
// truth), alarm rules and alarms, audit, settings, AI, MQTT, push, iPhone
// pairing, and the HTTP APIs (the dashboard's /api/v1, the iPhone companion
// API, and the authenticated gateway to video).
//
// It reaches recording servers only through link.Recorder (config
// snapshots, commands, status, the video plane) and hears from them only
// through the link.Sink it implements (sink.go). It never imports
// internal/recorder; cmd/sentinel joins the two (--role all).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/bus"
	"sentinel-linux/internal/onvif"
	"sentinel-linux/internal/store"
	"sentinel-linux/internal/tunnel"
)

const (
	sessionCookie = "sentinel_session"
	sessionTTL    = 12 * time.Hour
)

type Server struct {
	Store   *store.Store
	WebFS   fs.FS // built dashboard (index.html + assets); nil in API-only dev runs
	Version string

	limiter *auth.Limiter
	scanMu  sync.Mutex // one network scan at a time
	// DescribeStream checks guessed second-stream addresses for cameras on
	// this server (v2_streams.go); nil = onvif.DescribeVideo. Tests fake it.
	DescribeStream onvif.DescribeFunc
	dir            dirState // directory sign-in: LDAP, OIDC (v2_directory.go)

	// iPhone companion
	Tunnel     *tunnel.Tunnel  // nil = remote access unsupported
	Ctx        context.Context // server lifetime, for starting the tunnel
	ServerName string          // shown in the phone app; defaults to hostname
	ListenPort string          // dashboard/API port, baked into pairing QR codes
	pairing    pairingState
	ptz        ptzCache // per-camera PTZ probe + session (ptz.go)
	// two-way audio probes + sessions (talkback.go, WAVE9(B))
	talk talkbackState

	// Recording servers (recorders.go): the local one and/or remote ones.
	// Control is the control link listener (controllink.go); nil = remote
	// recording servers can't connect.
	Control ControlLink
	recs    recorderSet
	cfgRev  atomic.Int64
	health  healthState  // Camera Offline tracking (sink.go)
	storage storageState // open Storage alarms (storage_alarm.go)

	// Low-latency live view (WHEP proxy); see webrtc.go
	webrtc webrtcState

	// AI (bring-your-own Anthropic key)
	AIOptions []option.RequestOption // tests point the client at a fake API
	ai        aiState

	// Feedback
	FeedbackURL   string             // "" = DefaultFeedbackURL
	RecentLog     func() string      // recent server log lines, secrets stripped
	Logs          func(n int) string // last n log lines, secrets stripped (the local recording server's log)
	feedbackLimit feedbackLimiter

	// Update checks
	UpdateURL string // "" = DefaultUpdateURL
	updates   updateState

	// MQTT + Home Assistant (mqtt.go)
	mqtt mqttState

	// Alarm rules + iPhone push (rules.go, push.go)
	rules rulesState
	push  pushState
	hooks webhookState // webhooks (v2_webhooks.go)

	// Prepared clip downloads (playback.go)
	exports exportsState

	// Long and scheduled exports (export_jobs.go, export_sched.go; WAVE7(C))
	exportJobs exportJobsState

	// Appearance search: optional re-id model + index (appearance.go; WAVE9(A))
	appearance appearanceState

	// Model catalog downloads (v2_models.go; WAVE16 stream A)
	models modelsState
	// Face recognition: optional face models + people gallery (faces.go; wave16-faces)
	faces facesState

	// HomeKit is the Apple Home bridge (homekit.go); nil outside --role all.
	HomeKit HomeKitBridge

	// Bus carries what happens on the site (events, alarms, camera state,
	// config changes) to MQTT, push and, in API v2, the WebSocket stream.
	Bus *bus.Bus

	// Edge recording recovery jobs (edge_recovery.go; WAVE9(C))
	edgeJobs  edgeJobsState
	usbMu     sync.Mutex
	usbClient *http.Client // nil uses the local Unix-socket broker; injectable in tests
}

// New creates the management side. It has no recording server until
// AttachRecorder or ConnectRecorder is called.
func New(st *store.Store, web fs.FS, version string) *Server {
	s := &Server{Store: st, WebFS: web, Version: version, limiter: auth.NewLimiter(), Bus: bus.New()}
	// Every newly opened alarm, whoever raised it, goes on the bus once it
	// is committed.
	st.OnNewAlarm(func(a store.Alarm) { bus.Publish(s.Bus, TopicAlarmNew, a) })
	s.subscribeMQTT()
	return s
}

type ctxKey int

const userKey ctxKey = 0

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", s.handleLogout)

	// Signed in
	mux.Handle("GET /api/v1/me", s.authed(auth.ViewLive, s.handleMe))
	mux.Handle("GET /api/v1/cameras", s.authed(auth.ViewLive, s.handleListCameras))
	mux.Handle("POST /api/v1/cameras", s.authed(auth.ManageCameras, s.handleSaveCamera))
	mux.Handle("PUT /api/v1/cameras/{id}", s.authed(auth.ManageCameras, s.handleSaveCamera))
	mux.Handle("DELETE /api/v1/cameras/{id}", s.authed(auth.ManageCameras, s.handleDeleteCamera))
	mux.Handle("POST /api/v1/discover", s.authed(auth.ManageCameras, s.handleDiscover))
	mux.Handle("POST /api/v1/discover/probe", s.authed(auth.ManageCameras, s.handleDiscoverProbe))
	mux.Handle("GET /api/v1/cameras/{id}/recordings", s.authed(auth.ViewLive, s.inScope("id", s.handleListRecordings)))
	mux.Handle("GET /api/v1/users", s.authed(auth.ManageUsers, s.handleListUsers))
	mux.Handle("POST /api/v1/users", s.authed(auth.ManageUsers, s.handleCreateUser))
	mux.Handle("DELETE /api/v1/users/{id}", s.authed(auth.ManageUsers, s.handleDeleteUser))
	mux.Handle("GET /api/v1/settings", s.authed(auth.ViewLive, s.handleGetSettings))
	mux.Handle("PUT /api/v1/settings", s.authed(auth.ChangeSettings, s.handlePutSettings))
	mux.Handle("GET /api/v1/system", s.authed(auth.ViewLive, s.handleSystem))
	mux.Handle("GET /api/v1/update", s.authed(auth.ViewLive, s.handleGetUpdate))
	mux.Handle("PUT /api/v1/update", s.authed(auth.ChangeSettings, s.handlePutUpdate))
	mux.Handle("GET /api/v1/hwaccel", s.authed(auth.ViewLive, s.handleGetHWAccel))
	mux.Handle("PUT /api/v1/hwaccel", s.authed(auth.ChangeSettings, s.handlePutHWAccel))
	mux.Handle("GET /api/v1/audit", s.authed(auth.ViewAudit, s.siteWide(s.handleAudit))) // scope_sitewide.go
	mux.Handle("GET /api/v1/audit/verify", s.authed(auth.ViewAudit, s.handleAuditVerify))

	mux.Handle("GET /api/v1/alarms", s.authed(auth.ViewLive, s.handleListAlarms))
	mux.Handle("POST /api/v1/alarms/{id}/state", s.authed(auth.AcknowledgeAlarms, s.handleSetAlarmState))
	mux.Handle("POST /api/v1/alarms/{id}/evidence", s.authed(auth.ExportEvidence, s.handleLockEvidence))
	mux.Handle("GET /api/v1/events", s.authed(auth.ViewLive, s.handleListEvents))
	mux.Handle("GET /api/v1/events/{id}/thumbnail.jpg", s.authed(auth.ViewLive, func(w http.ResponseWriter, r *http.Request) {
		s.serveEventThumbnail(w, r, r.PathValue("id"))
	}))
	mux.Handle("GET /api/v1/evidence", s.authed(auth.ExportEvidence, s.handleListEvidence))
	mux.Handle("GET /api/v1/evidence/{id}/file", s.authed(auth.ExportEvidence, s.handleEvidenceFile))
	mux.Handle("POST /api/v1/feedback", s.authed(auth.ViewLive, s.handleFeedback))
	mux.Handle("GET /api/v1/ai", s.authed(auth.ViewLive, s.handleGetAI))
	mux.Handle("PUT /api/v1/ai", s.authed(auth.ChangeSettings, s.handlePutAI))
	mux.Handle("POST /api/v1/ai/search", s.authed(auth.AcknowledgeAlarms, s.handleAISearch))
	mux.Handle("POST /api/v1/ai/digest", s.authed(auth.AcknowledgeAlarms, s.handleAIDigest))
	mux.Handle("GET /api/v1/pairing", s.authed(auth.ManageUsers, s.handleGetPairing))
	mux.Handle("POST /api/v1/pairing", s.authed(auth.ManageUsers, s.handleStartPairing))
	mux.Handle("DELETE /api/v1/pairing", s.authed(auth.ManageUsers, s.handleCancelPairing))
	mux.Handle("GET /api/v1/devices", s.authed(auth.ManageUsers, s.handleListDevices))
	mux.Handle("DELETE /api/v1/devices/{id}", s.authed(auth.ManageUsers, s.handleRevokeDevice))
	mux.Handle("GET /api/v1/remote", s.authed(auth.ManageUsers, s.handleGetRemote))
	mux.Handle("PUT /api/v1/remote", s.authed(auth.ChangeSettings, s.handlePutRemote))
	mux.Handle("GET /api/v1/mqtt", s.authed(auth.ChangeSettings, s.handleGetMQTT))
	mux.Handle("PUT /api/v1/mqtt", s.authed(auth.ChangeSettings, s.handlePutMQTT))
	mux.Handle("POST /api/v1/mqtt/test", s.authed(auth.ChangeSettings, s.handleTestMQTT))

	// Client API v2 (v2.go; contract api/openapi.yaml)
	s.registerV2(mux)

	// iPhone companion API (Sentinel Mobile) — bearer-token auth, Mac-compatible paths
	s.registerCompanion(mux)

	// Alarm rules + push settings
	s.registerRules(mux)

	// Timeline playback & clip export (M2)
	s.registerPlayback(mux)

	// Apple Home bridge settings (homekit.go)
	s.registerHomeKit(mux)

	// Video (cookie-authenticated so <video> and hls.js just work)
	mux.Handle("GET /live/{id}/{file}", s.authed(auth.ViewLive, s.inScope("id", s.handleLiveProxy)))
	mux.Handle("GET /recordings/{id}/{file}", s.authed(auth.ViewLive, s.inScope("id", s.handleRecordingFile)))
	s.registerWebRTC(mux) // low-latency live view (WHEP), webrtc.go

	// Dashboard
	mux.Handle("/", s.spa())

	// WAVE13(B): the dashboard may also call Supabase (Sentinel Cloud sign-in, checkout, portal).
	return s.securityHeadersWith(s.cloudConnectSrc, s.csrfGuard(mux))
}

// MARK: - Middleware

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return s.securityHeadersWith(nil, next)
}

// securityHeadersWith is securityHeaders with more origins in connect-src:
// connect() returns them space-separated ("" for none).
func (s *Server) securityHeadersWith(connect func() string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		src := "'self'"
		if connect != nil {
			if more := connect(); more != "" {
				src += " " + more
			}
		}
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self' 'unsafe-inline'; connect-src "+src+"; worker-src 'self' blob:; frame-ancestors 'none'") // WAVE13(B): connect-src
		next.ServeHTTP(w, r)
	})
}

// csrfGuard: state-changing API calls must be JSON (a cross-site form can't
// send that without a CORS preflight we never grant) and, when the browser
// says where it came from, come from this same host.
func (s *Server) csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") {
			// Requests that carry no cookie can't be forged by another site:
			// API v2 bearer calls and token exchanges skip the Origin check.
			noCookie := bearerToken(r) != "" || strings.HasPrefix(r.URL.Path, "/api/v2/auth/")
			if origin := r.Header.Get("Origin"); origin != "" && !noCookie {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "cross-site request refused")
					return
				}
			}
			// The Mac importer's evidence upload is a file body, accepted only
			// with a bearer token (import_mac.go).
			upload := bearerToken(r) != "" && strings.HasPrefix(r.URL.Path, macEvidenceUploadPath)
			// The floorplan upload is multipart: bearer calls, or cookie calls
			// that set a custom header (a cross-site form can't) — floorplans.go.
			// Video upload parts (v2_uploads.go, WAVE23): bearer calls, or
			// cookie calls with the custom header.
			upload = upload || isUploadPart(r)
			upload = upload || (r.URL.Path == floorplanUploadPath && (bearerToken(r) != "" || r.Header.Get(floorplanUploadHeader) != ""))
			// A request relayed through Sentinel Cloud arrives with an unknown
			// length (-1) even when it has no body, so a bodiless POST such as
			// the phone camera's push-token or an upload's complete looked like
			// a non-JSON body and was refused (WAVE23). No Content-Type at all
			// can't be a cross-site form (browsers always set one), so treat
			// unknown length without a type as no body.
			hasBody := r.ContentLength > 0 || (r.ContentLength < 0 && r.Header.Get("Content-Type") != "")
			if hasBody && !upload && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "send JSON")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(p auth.Permission, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		u, err := s.Store.SessionUser(r.Context(), c.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session expired — sign in again")
			return
		}
		if !s.can(u.Role, p) {
			writeError(w, http.StatusForbidden, "your role ("+u.Role+") can't do this")
			return
		}
		h(w, r.WithContext(withScope(context.WithValue(r.Context(), userKey, u), u)))
	})
}

// MARK: - Helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// inputError is a validation failure found by logic shared between /api/v1
// and /api/v2; both answer 400 with its message.
type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

// conflictError: the request clashes with the current state (the last
// administrator, deleting yourself, a taken name). v2 answers 409; v1 keeps
// the 400 it always sent.
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) audit(r *http.Request, area, action, detail string) {
	user := "System"
	if u := currentUser(r); u != nil {
		user = u.Name
	}
	if err := s.Store.Audit(r.Context(), user, area, action, detail); err != nil {
		slog.Error("audit write failed", "err", err)
	}
}

// MARK: - Session handlers

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.UserCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := map[string]any{"setupRequired": n == 0, "version": s.Version}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			resp["user"] = u
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type credentials struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// handleSetup creates the first Admin. Only works while no users exist.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !readJSON(w, r, &in) {
		return
	}
	if n, _ := s.Store.UserCount(r.Context()); n > 0 {
		writeError(w, http.StatusConflict, "setup is already complete")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "enter a name")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := s.Store.CreateUser(r.Context(), name, "Admin", hash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.Store.Audit(r.Context(), u.Name, "Users", "Created first administrator", u.Name)
	s.startSession(w, r, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if !readJSON(w, r, &in) {
		return
	}
	nameKey, ipKey := "user:"+strings.ToLower(strings.TrimSpace(in.Name)), "ip:"+clientIP(r)
	if locked, until := s.limiter.Locked(nameKey, ipKey); locked {
		writeError(w, http.StatusTooManyRequests, "too many failed sign-ins — try again after "+until.Format("15:04"))
		return
	}
	// Local users first, then the directory (v2_directory.go).
	u, via, err := s.passwordSignIn(r.Context(), in.Name, in.Password)
	if err != nil {
		f := signInFailed(err)
		if f.counts {
			s.limiter.Fail(nameKey, ipKey)
		}
		_ = s.Store.Audit(r.Context(), strings.TrimSpace(in.Name), "Users", "Failed sign-in", f.audit+"from "+clientIP(r))
		writeError(w, f.status, f.msg)
		return
	}
	s.limiter.Reset(nameKey)
	_ = s.Store.Audit(r.Context(), u.Name, "Users", "Signed in", via+"from "+clientIP(r))
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	if err := s.setSessionCookie(w, r, u); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

// setSessionCookie starts a dashboard session for u.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, u *store.User) error {
	token, err := s.Store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Behind the Cloudflare tunnel the browser sees https even though this
		// hop is plain http.
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: int(sessionTTL.Seconds()),
	})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, err := s.Store.SessionUser(r.Context(), c.Value); err == nil {
			_ = s.Store.Audit(r.Context(), u.Name, "Users", "Signed out", "")
		}
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

// MARK: - Users

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.Users(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Role     string `json:"role"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	u, err := s.createUser(r, in.Name, in.Role, in.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// createUser validates and adds a user, and audits it (v1 and v2). A
// taken name is a conflictError, other bad input an inputError.
func (s *Server) createUser(r *http.Request, name, role, password string) (*store.User, error) {
	if strings.TrimSpace(name) == "" || !s.Store.RoleExists(r.Context(), role) {
		return nil, inputError{"enter a name and a valid role"}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, inputError{err.Error()}
	}
	if _, err := s.Store.UserByName(r.Context(), name); err == nil {
		return nil, conflictError{fmt.Sprintf("a user named %q already exists", strings.TrimSpace(name))}
	}
	u, err := s.Store.CreateUser(r.Context(), name, role, hash)
	if err != nil {
		return nil, err
	}
	s.audit(r, "Users", "Created user", u.Name+" ("+u.Role+")")
	return u, nil
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	var conflict conflictError
	switch err := s.deleteUser(r, r.PathValue("id")); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.As(err, &conflict):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// deleteUser removes a user and audits it (v1 and v2). Nobody may delete
// themselves or the last administrator (conflictError).
func (s *Server) deleteUser(r *http.Request, id string) error {
	target, err := s.Store.UserByID(r.Context(), id)
	if err != nil {
		return store.ErrNotFound
	}
	if target.ID == currentUser(r).ID {
		return conflictError{"you can't delete yourself"}
	}
	if target.Role == "Admin" {
		if n, _ := s.Store.AdminCount(r.Context()); n <= 1 {
			return conflictError{"can't delete the last administrator"}
		}
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		return err
	}
	s.audit(r, "Users", "Deleted user", target.Name)
	return nil
}

// MARK: - Audit

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.AuditLog(r.Context(), 1000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	checked, broken, err := s.Store.VerifyAudit(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checked": checked, "intact": broken == "", "brokenEntryID": broken})
}

// MARK: - Dashboard (single-page app)

func (s *Server) spa() http.Handler {
	if s.WebFS != nil {
		if _, err := fs.Stat(s.WebFS, "index.html"); err != nil {
			s.WebFS = nil // fresh checkout: only the placeholder is embedded
		}
	}
	if s.WebFS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dashboard not built — run scripts/build.sh", http.StatusNotFound)
		})
	}
	files := http.FileServer(http.FS(s.WebFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := s.WebFS.Open(p); err == nil {
				f.Close()
				if strings.HasPrefix(p, "assets/") {
					// Vite fingerprints asset names, so they can be cached forever.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			} else if !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// Client-side routes all load index.html.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
