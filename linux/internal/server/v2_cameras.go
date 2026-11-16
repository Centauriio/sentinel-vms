package server

// API v2: cameras, their timeline and recorded segments.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sentinel-linux/internal/store"
)

func (s *Server) handleV2Cameras(w http.ResponseWriter, r *http.Request) {
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeV2Err(w, err)
		return
	}
	cams = s.visibleCameras(r.Context(), cams)
	statuses := s.statuses()
	out := make([]v2Camera, 0, len(cams))
	for i := range cams {
		st, connected := statuses.of(cams[i].RecorderID)
		out = append(out, toV2Camera(&cams[i], st, connected))
	}
	s.fillCameraGroups(r.Context(), out)
	v2Items(w, out)
}

func (s *Server) handleV2Camera(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.Camera(r.Context(), r.PathValue("cameraId"))
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	writeJSON(w, http.StatusOK, s.withCameraGroups(r.Context(), s.v2CameraView(c)))
}

// v2CameraView is a camera with its recording server's status.
func (s *Server) v2CameraView(c *store.Camera) v2Camera {
	st, connected := s.statusOf(c.RecorderID)
	return toV2Camera(c, st, connected)
}

// v2RecorderChoice checks a recorderId sent with a camera: a recording
// server in the registry that isn't revoked (it may be offline).
func (s *Server) v2RecorderChoice(ctx context.Context, id string) error {
	rs, err := s.Store.RecordingServer(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return inputError{"no recording server " + strconv.Quote(id)}
	} else if err != nil {
		return err
	}
	if rs.Revoked() {
		return inputError{"recording server " + rs.Name + " was revoked"}
	}
	return nil
}

// moveCamera assigns a camera to another recording server and audits it;
// footage recorded so far stays on the old one (store placements). The
// caller then applies the configuration (both servers get new snapshots).
func (s *Server) moveCamera(r *http.Request, c *store.Camera, to string) error {
	if c.RecorderID == to {
		return nil
	}
	instance, err := s.Store.USBInstance(r.Context(), c.ID)
	if err != nil {
		return err
	}
	if instance != "" {
		return inputError{"USB cameras must stay on their local recording server."}
	}
	if err := s.Store.SetCameraRecorder(r.Context(), c.ID, to); err != nil {
		return err
	}
	s.audit(r, "Cameras", "Moved camera", c.Name+": "+s.recorderName(r.Context(), c.RecorderID)+" → "+s.recorderName(r.Context(), to))
	return nil
}

// recorderName is a recording server's name for audit entries.
func (s *Server) recorderName(ctx context.Context, id string) string {
	if rs, err := s.Store.RecordingServer(ctx, id); err == nil && rs.Name != "" {
		return rs.Name
	}
	return id
}

// v2CameraFields is CameraInput and CameraPatch: nil means "not sent".
type v2CameraFields struct {
	Name             *string `json:"name"`
	Location         *string `json:"location"`
	RTSPURL          *string `json:"rtspUrl"`
	SubRTSPURL       *string `json:"subRtspUrl"`
	Username         *string `json:"username"`
	Password         *string `json:"password"`
	Recording        *bool   `json:"recording"`
	RetentionDays    *int    `json:"retentionDays"`
	MotionLevel      *int    `json:"motionLevel"`
	AlertOn          *string `json:"alertOn"`
	ONVIFURL         *string `json:"onvifUrl"`
	CameraAnalytics  *bool   `json:"cameraAnalytics"`
	AudioDetect      *bool   `json:"audioDetect"`      // audio event detection switch (audio.go)
	AudioThresholdDB *int    `json:"audioThresholdDb"` // dB over the noise floor, 6..60
	LoiterSeconds    *int    `json:"loiterSeconds"`    // loitering time, 0 (off)..3600 (loiter.go)
	SortOrder        *int    `json:"sortOrder"`        // PATCH only
	RecorderID       *string `json:"recorderId"`       // assigns (POST) or moves (PATCH)

	// Recording modes (WAVE22, recording_modes.go).
	RecordingMode       *string      `json:"recordingMode"`
	RecordTrigger       *string      `json:"recordTrigger"`
	PreRollSeconds      store.OptInt `json:"preRollSeconds"`  // null = server default, number (0 too) literal
	PostRollSeconds     store.OptInt `json:"postRollSeconds"` // absent = unchanged
	LowResRetentionDays *int         `json:"lowResRetentionDays"`

	GroupIDs *[]string `json:"groupIds"` // replaces the camera's groups

	// Source (WAVE23) can't be changed and only "rtsp" cameras are added
	// here (iPhone cameras: POST /api/v2/phone-cameras).
	Source *string `json:"source"`
}

// checkSource refuses a source other than the camera's (cur "" = new camera).
func (f *v2CameraFields) checkSource(cur string) error {
	if f.Source == nil {
		return nil
	}
	if cur == "" {
		if *f.Source == "rtsp" {
			return nil
		}
		if *f.Source == "phone" {
			return inputError{"add an iPhone camera with POST /api/v2/phone-cameras"}
		}
		return inputError{"source must be rtsp"}
	}
	if *f.Source != cur {
		return inputError{"a camera's source can't be changed"}
	}
	return nil
}

// apply copies the sent fields over in. Credentials typed into a new
// rtspUrl replace the stored ones unless username/password are sent too
// (normalizeCamera moves them out of the URL).
func (f *v2CameraFields) apply(in *store.CameraInput) error {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&in.Name, f.Name)
	set(&in.Location, f.Location)
	set(&in.SubRTSPURL, f.SubRTSPURL)
	set(&in.Username, f.Username)
	if f.RTSPURL != nil {
		in.RTSPURL = *f.RTSPURL
		if u, err := url.Parse(strings.TrimSpace(*f.RTSPURL)); err == nil && u.User != nil && f.Username == nil {
			in.Username = ""
		}
	}
	if f.Password != nil {
		in.Password = f.Password
	}
	// recordingMode wins; recording alone (older clients) switches the
	// mode off, or on to the camera's mode ("default" if it was off).
	switch {
	case f.RecordingMode != nil:
		in.RecordingMode = f.RecordingMode
	case f.Recording != nil:
		in.Recording, in.RecordingMode = *f.Recording, nil
	}
	if f.RecordTrigger != nil {
		in.RecordTrigger = f.RecordTrigger
	}
	if f.PreRollSeconds.Set {
		in.PreRollSeconds = f.PreRollSeconds
	}
	if f.PostRollSeconds.Set {
		in.PostRollSeconds = f.PostRollSeconds
	}
	if f.LowResRetentionDays != nil {
		in.LowResRetentionDays = f.LowResRetentionDays
	}
	if f.RetentionDays != nil {
		in.RetentionDays = *f.RetentionDays
	}
	if f.MotionLevel != nil {
		in.MotionLevel = f.MotionLevel
	}
	if f.AlertOn != nil {
		v, ok := alertOnValue(*f.AlertOn)
		if !ok {
			return inputError{"alertOn must be auto, motion, peopleAndVehicles or people"}
		}
		in.AlertOn = &v
	}
	if f.ONVIFURL != nil {
		in.ONVIFURL = f.ONVIFURL
	}
	if f.CameraAnalytics != nil {
		in.CameraAnalytics = f.CameraAnalytics
	}
	in.AudioDetect, in.AudioThresholdDB = f.AudioDetect, f.AudioThresholdDB
	in.LoiterSeconds = f.LoiterSeconds
	return nil
}

func (s *Server) handleV2CreateCamera(w http.ResponseWriter, r *http.Request) {
	var f v2CameraFields
	if !readV2JSON(w, r, &f) {
		return
	}
	if f.SortOrder != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, "sortOrder can only be changed on an existing camera")
		return
	}
	in := store.CameraInput{Recording: true} // other defaults are SaveCamera's
	if err := f.checkSource(""); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if err := f.apply(&in); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if f.RecorderID != nil {
		if err := s.v2RecorderChoice(r.Context(), *f.RecorderID); err != nil {
			writeV2Fail(w, err, "recording server")
			return
		}
		in.RecorderID = *f.RecorderID
	} else {
		id, ok := s.defaultRecorderID(r.Context())
		if !ok {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, "this site has several recording servers: choose one (recorderId)")
			return
		}
		in.RecorderID = id
	}
	if err := s.checkGroupIDs(r.Context(), f.GroupIDs); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	cam, err := s.saveCamera(r, "", in)
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if err := s.setCameraGroups(r, cam, f.GroupIDs); err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.withCameraGroups(r.Context(), s.v2CameraView(cam)))
}

// handleV2PatchCamera changes only the fields sent; the rest keep their
// stored values (password included: "" clears it, absent keeps it).
func (s *Server) handleV2PatchCamera(w http.ResponseWriter, r *http.Request) {
	var f v2CameraFields
	if !readV2JSON(w, r, &f) {
		return
	}
	id := r.PathValue("cameraId")
	cur, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	in := store.CameraInput{
		Name: cur.Name, Location: cur.Location, RTSPURL: cur.RTSPURL, SubRTSPURL: cur.SubRTSPURL, Username: cur.Username,
		Recording: cur.Recording, RetentionDays: cur.RetentionDays, MotionLevel: &cur.MotionLevel, AlertOn: &cur.AlertOn,
		ONVIFURL: &cur.ONVIFURL, CameraAnalytics: &cur.CameraAnalytics,
		RecordingMode: &cur.RecordingMode, // recording alone clears it (apply)
		Source:        cur.Source,
	}
	if err := f.checkSource(cur.Source); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if err := f.apply(&in); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if err := s.checkGroupIDs(r.Context(), f.GroupIDs); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if f.RecorderID != nil {
		if err := s.v2RecorderChoice(r.Context(), *f.RecorderID); err != nil {
			writeV2Fail(w, err, "recording server")
			return
		}
	}
	if f.SortOrder != nil || f.RecorderID != nil {
		// Validate first so a rejected edit doesn't half-apply; saveCamera
		// then sends the configuration with the new order or placement
		// (every connected server, so the old one drops the camera).
		check := in
		if err := normalizeCamera(&check); err != nil {
			writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
			return
		}
	}
	if f.SortOrder != nil {
		if err := s.Store.SetCameraSortOrder(r.Context(), id, *f.SortOrder); err != nil {
			writeV2Fail(w, err, "camera")
			return
		}
	}
	if f.RecorderID != nil {
		if err := s.moveCamera(r, cur, *f.RecorderID); err != nil {
			writeV2Fail(w, err, "camera")
			return
		}
	}
	cam, err := s.saveCamera(r, id, in)
	if err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	if err := s.setCameraGroups(r, cam, f.GroupIDs); err != nil {
		writeV2Err(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.withCameraGroups(r.Context(), s.v2CameraView(cam)))
}

func (s *Server) handleV2DeleteCamera(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteCamera(r, r.PathValue("cameraId")); err != nil {
		writeV2Fail(w, err, "camera")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MARK: - Recordings

type v2Span struct {
	Start      int64   `json:"start"`
	End        int64   `json:"end"`
	Media      float64 `json:"media"`
	RecorderID string  `json:"recorderId"`
	Edge       bool    `json:"edge,omitempty"`    // WAVE9(C): recovered from the camera's card
	Quality    string  `json:"quality,omitempty"` // WAVE22: "full" | "low"; omitted = full
}

type v2Timeline struct {
	From    int64     `json:"from"`
	To      int64     `json:"to"`
	Now     int64     `json:"now"`
	Spans   []v2Span  `json:"spans"`
	Pending []v2Span  `json:"pending"`
	Writing bool      `json:"writing"`
	Events  []v2Event `json:"events"`
	Alarms  []v2Alarm `json:"alarms"`
}

func toV2Spans(spans []footageSpan) []v2Span {
	out := make([]v2Span, 0, len(spans))
	for _, sp := range spans {
		out = append(out, v2Span{Start: sp.Start, End: sp.End, Media: sp.Media, RecorderID: sp.RecorderID, Edge: sp.Edge, Quality: sp.Quality})
	}
	return out
}

type v2Segment struct {
	Name       string `json:"name"`
	Start      int64  `json:"start"`
	End        int64  `json:"end"`
	Size       int64  `json:"size"`
	Writing    bool   `json:"writing"`
	RecorderID string `json:"recorderId"`
}

// v2CameraExists answers 404 itself when the camera isn't there.
func (s *Server) v2CameraExists(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("cameraId")
	if _, err := s.Store.Camera(r.Context(), id); err != nil {
		writeV2Fail(w, err, "camera")
		return id, false
	}
	return id, true
}

func (s *Server) handleV2Timeline(w http.ResponseWriter, r *http.Request) {
	id, ok := s.v2CameraExists(w, r)
	if !ok {
		return
	}
	from, to, err := parseWindow(r, maxTimelineWindow)
	if err != nil {
		writeV2Error(w, http.StatusBadRequest, codeBadRequest, err.Error())
		return
	}
	tl, err := s.recordedTimeline(r.Context(), id, from, to)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	events, alarms, err := s.timelineMarkers(r.Context(), id, from, to)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := v2Timeline{
		From: ms(from), To: ms(to), Now: ms(time.Now()), Writing: tl.Writing,
		Spans: toV2Spans(tl.Spans), Pending: toV2Spans(tl.Pending),
		Events: make([]v2Event, 0, len(events)), Alarms: make([]v2Alarm, 0, len(alarms)),
	}
	for i := range events {
		out.Events = append(out.Events, toV2Event(&events[i]))
	}
	for i := range alarms {
		out.Alarms = append(out.Alarms, toV2Alarm(&alarms[i], nil))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleV2Recordings(w http.ResponseWriter, r *http.Request) {
	id, ok := s.v2CameraExists(w, r)
	if !ok {
		return
	}
	segs, err := s.listSegments(r.Context(), id)
	if err != nil {
		writeV2Err(w, err)
		return
	}
	out := make([]v2Segment, 0, len(segs))
	for _, g := range segs {
		out = append(out, v2Segment{Name: g.Name, Start: secToMs(g.Start), End: secToMs(g.End), Size: g.Size, Writing: g.Writing, RecorderID: g.RecorderID})
	}
	v2Items(w, out)
}
