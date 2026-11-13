package server

// Client API v2 (PLATFORM.md, "Client API v2"). The contract is
// api/openapi.yaml; TestOpenAPIMatchesRoutes keeps the two in step.
//
// Every v2 route is registered through s.v2Route, which records it for that
// test and wraps it in authedV2 (bearer access token or the dashboard's
// session cookie, then the role check). Handlers answer errors with
// writeV2Error so clients always get {"error":{"code","message"}}.
//
// Files by area (each registers its own routes):
//
//	v2.go            middleware, errors, JSON helpers, the spec route
//	v2_auth.go       /auth/token, /auth/revoke, /me
//	v2_views.go      the v2 JSON shapes and converters from store types
//	v2_events.go     /events/stream (WebSocket)
//	v2_video.go      /cameras/{id}/video-token and the /v/{token}/ gateway
//	v2_resources.go  cameras, alarms, events, recordings, rules, users,
//	                 recording servers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"sentinel-linux/api"
	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

// v2 error codes (api/openapi.yaml, Error.code).
const (
	codeBadRequest   = "bad_request"
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeNotFound     = "not_found"
	codeConflict     = "conflict"
	codeTooMany      = "too_many_requests"
	codeUnavailable  = "unavailable"
	codeTimeout      = "timeout"
	codeTokenReused  = "token_reused"
	codeInternal     = "internal"
)

// v2Routes records every registered v2 route ("GET /api/v2/cameras/{id}")
// for TestOpenAPIMatchesRoutes. Filled while building the handler.
var v2Routes = map[string]bool{}

const publicRoute auth.Permission = -1

// v2Route registers a v2 route. p is the permission it needs, or
// publicRoute for the unauthenticated ones (sign-in, the spec).
func (s *Server) v2Route(mux *http.ServeMux, pattern string, p auth.Permission, h http.HandlerFunc) {
	v2Routes[pattern] = true
	if p == publicRoute {
		mux.HandleFunc(pattern, h)
		return
	}
	mux.Handle(pattern, s.authedV2(p, h))
}

func (s *Server) registerV2(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/openapi.yaml", publicRoute, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(api.OpenAPI)
	})
	s.registerV2Auth(mux)       // v2_auth.go
	s.registerV2Events(mux)     // v2_events.go
	s.registerV2Video(mux)      // v2_video.go
	s.registerV2Resources(mux)  // v2_resources.go
	s.registerV2Groups(mux)     // v2_groups.go
	s.registerV2Audit(mux)      // v2_audit.go
	s.registerV2Import(mux)     // import_mac.go
	s.registerV2Push(mux)       // v2_push.go
	s.registerV2Search(mux)     // v2_search.go
	s.registerV2PTZ(mux)        // ptz.go
	s.registerV2Floorplans(mux) // floorplans.go
	s.registerV2Encoder(mux)    // v2_encoder.go
	s.registerV2Streams(mux)    // v2_streams.go
	s.registerV2Evidence(mux)   // v2_evidence.go
	s.registerV2Share(mux)      // share.go (+ share_public.go)
	s.registerV2Incidents(mux)  // v2_incidents.go (+ v2_alarms_manager.go)
	s.registerV2Roles(mux)      // v2_roles.go
	s.registerV2System(mux)     // v2_system.go, v2_config_report.go
	s.registerV2Recording(mux)  // recording_modes.go (WAVE22)
	s.registerV2Zones(mux)      // zones.go

	s.registerV2TimeProfiles(mux) // v2_timeprofiles.go
	s.registerV2Webhooks(mux)     // v2_webhooks.go
	s.registerV2Bookmarks(mux)    // bookmarks.go

	s.registerV2ExportJobs(mux)      // export_jobs.go (WAVE7(C))
	s.registerV2ExportSchedules(mux) // export_sched.go (WAVE7(C))
	s.registerV2EvidencePackage(mux) // evidencepkg.go (WAVE8(C))
	s.registerV2Appearance(mux)      // v2_appearance.go (WAVE9(A))
	s.registerV2EdgeRecovery(mux)    // edge_recovery.go (WAVE9(C))
	s.registerV2Talkback(mux)        // talkback.go (WAVE9(B))
	s.registerV2DeviceCodes(mux)     // device_codes.go (WAVE10(C))
	s.registerV2LiveActivity(mux)    // liveactivity.go (WAVE10(B))
	s.registerV2WallLayouts(mux)     // wall_layouts.go (WAVE10(A))
	s.registerV2Cloud(mux)           // v2_cloud.go // WAVE12(C)
	// wave16-semantic
	s.registerV2Semantic(mux) // v2_semantic.go

	s.registerV2PhoneCameras(mux) // v2_phone_cameras.go (WAVE23)
	s.registerV2USB(mux)
	s.registerV2Uploads(mux) // v2_uploads.go (WAVE23)

	// Anything else under /api/v2/ is a JSON 404, not the dashboard.
	mux.HandleFunc("/api/v2/", func(w http.ResponseWriter, _ *http.Request) {
		writeV2Error(w, http.StatusNotFound, codeNotFound, "no such endpoint")
	})
}

// MARK: - Auth

type v2AuthKey int

const (
	accessTokenKey v2AuthKey = iota // the bearer access token, "" for cookie sessions
)

// bearerToken returns the request's "Authorization: Bearer" token, if any.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// v2User resolves the caller from a bearer access token or, failing that,
// the dashboard session cookie. token is the access token ("" for a
// cookie session).
func (s *Server) v2User(r *http.Request) (u *store.User, token string, err error) {
	if t := bearerToken(r); t != "" {
		u, err := s.Store.AccessTokenUser(r.Context(), t)
		return u, t, err
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, "", store.ErrNotFound
	}
	u, err = s.Store.SessionUser(r.Context(), c.Value)
	return u, "", err
}

func (s *Server) authedV2(p auth.Permission, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, token, err := s.v2User(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sentinel"`)
			writeV2Error(w, http.StatusUnauthorized, codeUnauthorized, "sign in required, or the token expired")
			return
		}
		if !s.can(u.Role, p) {
			writeV2Error(w, http.StatusForbidden, codeForbidden, "your role ("+u.Role+") can't do this")
			return
		}
		ctx := withScope(context.WithValue(r.Context(), userKey, u), u)
		ctx = context.WithValue(ctx, accessTokenKey, token)
		h(w, r.WithContext(ctx))
	})
}

// requestAccessToken is the bearer token an authedV2 request came with
// ("" for a cookie session).
func requestAccessToken(r *http.Request) string {
	t, _ := r.Context().Value(accessTokenKey).(string)
	return t
}

// MARK: - Errors and JSON

func writeV2Error(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

// writeV2Err maps an error from the store or a recorder command.
func writeV2Err(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeV2Error(w, http.StatusNotFound, codeNotFound, "not found")
	case errors.Is(err, context.DeadlineExceeded):
		writeV2Error(w, http.StatusGatewayTimeout, codeTimeout, "the recording server took too long")
	default:
		status := commandStatus(err) // recorder command failures (recorders.go)
		code := codeInternal
		switch status {
		case http.StatusBadRequest:
			code = codeBadRequest
		case http.StatusNotFound:
			code = codeNotFound
		case http.StatusConflict:
			code = codeConflict
		case http.StatusTooManyRequests:
			code = codeTooMany
		case http.StatusServiceUnavailable, http.StatusBadGateway:
			code = codeUnavailable
		case http.StatusGatewayTimeout:
			code = codeTimeout
		}
		writeV2Error(w, status, code, err.Error())
	}
}

// readV2JSON decodes a JSON body (≤ 1 MB, unknown fields rejected).
func readV2JSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "invalid request: "+err.Error())
		return false
	}
	return true
}
