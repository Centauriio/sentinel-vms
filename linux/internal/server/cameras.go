package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/link"
	"sentinel-linux/internal/store"
)

var (
	cameraIDPattern  = regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}$`)
	segmentPattern   = regexp.MustCompile(`^\d{8}-\d{6}-\d+\.mp4$`)
	hlsFilePattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.(m3u8|ts|mp4|m4s)$`)
	defaultRetention = "7"
)

// snapshot builds one recording server's desired configuration from the
// camera table and settings (the management side is the source of truth):
// the cameras assigned to it, and the site-wide settings. cams is every
// camera.
func (s *Server) snapshot(ctx context.Context, recorderID string, cams []store.Camera) link.Snapshot {
	snap := link.Snapshot{
		Rev:              s.cfgRev.Add(1),
		Cameras:          []link.CameraConfig{},
		WebRTCExtraHosts: splitHosts(s.Store.Setting(ctx, webrtcHostsKey, "")),
		HWAccel:          detect.AccelAuto,
		AIDescriptions:   s.aiReady(),
		VideoTokenKeys:   s.videoTokenKeys(ctx), // v2_video.go
		DetectorModel:    s.Store.Setting(ctx, SettingDetectorModel, ""),
	}
	if n, err := strconv.Atoi(s.Store.Setting(ctx, SettingDetectRate, "0")); err == nil {
		snap.DetectRate = n
	}
	if days, err := strconv.Atoi(s.Store.Setting(ctx, "default_retention_days", defaultRetention)); err == nil {
		snap.DefaultRetentionDays = days
	}
	snap.RecordingDefaults = s.Store.RecordingDefaults(ctx) // recording_modes.go (WAVE22)
	if s.Store.Setting(ctx, "hwaccel", detect.AccelAuto) == detect.AccelOff {
		snap.HWAccel = detect.AccelOff
	}
	if rs, err := s.Store.RecordingServer(ctx, recorderID); err == nil {
		snap.RecordingsDir = rs.RecordingsDir
	}
	imported := s.importedRecordings(ctx, recorderID) // import_mac.go
	zones, _ := s.Store.AllCameraZones(ctx)           // zones.go
	for i := range cams {
		c := &cams[i]
		if c.RecorderID != recorderID {
			continue
		}
		pass, err := s.Store.CameraPassword(c)
		if err != nil {
			slog.Error("camera password unreadable; skipping", "camera", c.Name, "err", err)
			continue
		}
		snap.Cameras = append(snap.Cameras, link.CameraConfig{
			ID: c.ID, Name: c.Name, RTSPURL: c.RTSPURL, SubRTSPURL: c.SubRTSPURL, Username: c.Username, Password: pass,
			Recording: c.Recording, RetentionDays: c.RetentionDays, MotionLevel: c.MotionLevel,
			ONVIFURL: c.ONVIFURL, CameraAnalytics: c.CameraAnalytics, ImportedRecordingsPath: imported[c.ID],
			Zones: store.LinkZones(zones[c.ID]), AudioDetect: audioThreshold(c), // audio.go
			LoiterSeconds: c.LoiterSeconds,
			RecordingMode: c.RecordingMode, RecordTrigger: c.RecordTrigger, PreRollSeconds: c.PreRollSeconds,
			PostRollSeconds: c.PostRollSeconds, LowResRetentionDays: c.LowResRetentionDays,
			Source: phoneSource(c), // WAVE23
		})
	}
	return snap
}

// ApplyMedia sends every connected recording server its configuration
// (MediaMTX config, detection readers, camera analytics) and announces it
// on the bus. Called at startup and after any camera, retention, live-view,
// acceleration or AI change. The first failure is returned; the others are
// still sent.
func (s *Server) ApplyMedia(ctx context.Context) error {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, id := range s.ConnectedRecorders() {
		if err := s.applyTo(ctx, id, cams); err != nil && first == nil {
			first = err
		}
	}
	s.publishConfigChanged(cams)
	return first
}

// ApplyRecorder sends one recording server its configuration (the control
// link calls it when a server connects).
func (s *Server) ApplyRecorder(ctx context.Context, id string) error {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	return s.applyTo(ctx, id, cams)
}

func (s *Server) applyTo(ctx context.Context, id string, cams []store.Camera) error {
	applied, err := s.recByID(id).ApplyConfig(ctx, s.snapshot(ctx, id, cams))
	if err != nil {
		slog.Warn("recording server did not take its configuration", "server", id, "err", err)
		return err
	}
	logApplyErrors(applied)
	return nil
}

type cameraView struct {
	store.Camera
	Online    bool   `json:"online"`
	Viewers   int    `json:"viewers"`
	LiveURL   string `json:"liveURL"`
	StatusMsg string `json:"status"`
	// AnalyticsStatus is the camera-analytics subscription state; "" = not subscribed.
	AnalyticsStatus string `json:"analyticsStatus,omitempty"`
}

func (s *Server) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cams = s.visibleCameras(r.Context(), cams)
	statuses := s.statuses()
	out := make([]cameraView, 0, len(cams))
	for _, c := range cams {
		status, connected := statuses.of(c.RecorderID) // the camera's own server
		st := status.Cameras[c.ID]
		v := cameraView{Camera: c, Online: st.Ready, Viewers: st.Readers, LiveURL: "/live/" + c.ID + "/index.m3u8", AnalyticsStatus: st.Analytics}
		switch {
		case !connected:
			v.Online, v.StatusMsg = false, "Recording server offline"
		case !status.Media.Running:
			v.StatusMsg = "Media engine starting"
		case c.IsPhone() && st.Push != nil && st.Push.Streaming: // WAVE23
			v.StatusMsg = "Streaming from " + st.Push.Device
		case c.IsPhone():
			v.StatusMsg = "Phone not streaming"
		case st.Ready:
			v.StatusMsg = "Online"
		default:
			v.StatusMsg = "No signal"
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// normalizeCamera validates input and moves any credentials typed into the URL
// into the username/password fields, so the stored URL never holds a secret.
func normalizeCamera(in *store.CameraInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Location = strings.TrimSpace(in.Location)
	if in.Name == "" {
		return errors.New("enter a camera name")
	}
	if in.Source == link.SourcePhone {
		// An iPhone pushes its video (WAVE23): no stream address, sign-in,
		// ONVIF or second stream. Empty values are accepted (clients send
		// the full camera form back).
		if strings.TrimSpace(in.RTSPURL) != "" || strings.TrimSpace(in.SubRTSPURL) != "" || strings.TrimSpace(in.Username) != "" ||
			(in.Password != nil && *in.Password != "") || (in.ONVIFURL != nil && strings.TrimSpace(*in.ONVIFURL) != "") {
			return errors.New("an iPhone camera has no stream address, sign-in, ONVIF address or second stream")
		}
		empty := ""
		in.RTSPURL, in.SubRTSPURL, in.Username, in.Password, in.ONVIFURL = "", "", "", &empty, &empty
		return validateCameraNumbers(in)
	}
	clean := func(raw string) (string, error) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (u.Scheme != "rtsp" && u.Scheme != "rtsps") || u.Host == "" {
			return "", errors.New("stream URL must look like rtsp://camera-address:554/path")
		}
		if u.User != nil {
			if in.Username == "" {
				in.Username = u.User.Username()
			}
			if p, ok := u.User.Password(); ok && in.Password == nil {
				in.Password = &p
			}
			u.User = nil
		}
		return u.String(), nil
	}
	var err error
	if in.RTSPURL, err = clean(in.RTSPURL); err != nil {
		return err
	}
	if strings.TrimSpace(in.SubRTSPURL) != "" {
		if in.SubRTSPURL, err = clean(in.SubRTSPURL); err != nil {
			return errors.New("sub-stream: " + err.Error())
		}
	} else {
		in.SubRTSPURL = ""
	}
	if in.ONVIFURL != nil {
		raw := strings.TrimSpace(*in.ONVIFURL)
		if raw != "" {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("ONVIF address must look like http://camera-address/onvif/device_service")
			}
		}
		in.ONVIFURL = &raw
	}
	return validateCameraNumbers(in)
}

// validateCameraNumbers checks the alert, motion and retention settings.
func validateCameraNumbers(in *store.CameraInput) error {
	if in.AlertOn != nil && (*in.AlertOn < 0 || *in.AlertOn > 3) {
		return errors.New("alert setting must be auto, any motion, people & vehicles or people only")
	}
	if in.MotionLevel != nil && (*in.MotionLevel < 0 || *in.MotionLevel > 3) {
		return errors.New("motion sensitivity must be off, low, medium or high")
	}
	if in.RetentionDays < 0 || in.RetentionDays > 3650 {
		return errors.New("retention must be between 0 and 3650 days")
	}
	return nil
}

func (s *Server) handleSaveCamera(w http.ResponseWriter, r *http.Request) {
	var in store.CameraInput
	if !readJSON(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		rid, ok := s.defaultRecorderID(r.Context()) // v1 can't pick a server
		if !ok {
			writeError(w, http.StatusBadRequest, "this site has several recording servers — add the camera in Sentinel Admin and choose one")
			return
		}
		in.RecorderID = rid
	}
	cam, err := s.saveCamera(r, id, in)
	var bad inputError
	if errors.As(err, &bad) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	} else if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, cam)
}

// saveCamera validates and stores a camera (id "" adds one), audits it and
// sends the new configuration to the recording server (v1 and v2).
func (s *Server) saveCamera(r *http.Request, id string, in store.CameraInput) (*store.Camera, error) {
	var before *store.Camera
	if id != "" {
		var err error
		if before, err = s.Store.Camera(r.Context(), id); err != nil {
			return nil, err
		}
		in.Source = before.Source // a camera's source never changes (WAVE23)
		instance, err := s.Store.USBInstance(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if instance != "" {
			if in.RTSPURL != before.RTSPURL || in.SubRTSPURL != before.SubRTSPURL || in.Username != before.Username || in.Password != nil || (in.RecorderID != "" && in.RecorderID != before.RecorderID) {
				return nil, inputError{"USB connection settings are managed by the USB add-on."}
			}
		}
	}
	if err := normalizeCamera(&in); err != nil {
		return nil, inputError{err.Error()}
	}
	if err := validateAudioInput(in); err != nil { // audio.go
		return nil, err
	}
	if err := validateLoiterInput(in); err != nil { // loiter.go
		return nil, err
	}
	if in.Source == link.SourcePhone && in.RecordingMode != nil && *in.RecordingMode == link.RecordDual {
		return nil, inputError{phoneNoDual}
	}
	if err := validateRecordingInput(in, before); err != nil { // recording_modes.go
		return nil, err
	}
	cam, err := s.Store.SaveCamera(r.Context(), id, in)
	if err != nil {
		return nil, err
	}
	if err := s.saveCameraAudio(r, cam, in); err != nil { // audio.go: persists, audits
		return nil, err
	}
	if err := s.saveCameraLoiter(r, cam, in); err != nil { // loiter.go: persists, audits
		return nil, err
	}
	s.ptz.forget(cam.ID) // address or credentials may have changed
	// talkback.go: re-probe two-way audio (WAVE9(B))
	s.talkbackCameraSaved(cam)
	if in.HomeKit != nil {
		if err := s.setCameraHomeKit(r.Context(), cam.ID, *in.HomeKit); err != nil {
			slog.Error("apple home flag not saved", "camera", cam.Name, "err", err)
		}
	}
	if id == "" {
		s.audit(r, "Cameras", "Added camera", cam.Name)
	} else {
		s.audit(r, "Cameras", "Edited camera", cam.Name)
		if change := recordingChange(before, cam); change != "" {
			s.audit(r, "Cameras", "Changed recording mode", change)
		}
	}
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	return cam, nil
}

func (s *Server) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	if err := s.deleteCamera(r, r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// deleteCamera removes a camera (keeping its recordings), audits it and
// reapplies the configuration (v1 and v2).
func (s *Server) deleteCamera(r *http.Request, id string) error {
	s.usbMu.Lock()
	defer s.usbMu.Unlock()
	cam, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		return store.ErrNotFound
	}
	instance, err := s.Store.USBInstance(r.Context(), id)
	if err != nil {
		return err
	}
	if instance != "" {
		if err := s.usbCall(r.Context(), "DELETE", "/cameras/"+instance, nil, nil); err != nil {
			return err
		}
	}
	if err := s.Store.DeleteCamera(r.Context(), id); err != nil {
		return err
	} // Recordings are kept on disk (deleting a camera must not destroy footage);
	// the recorder's sweeper deletes them under the server's default
	// retention and removes the folder once empty (recorder/retention.go).
	s.audit(r, "Cameras", "Deleted camera", cam.Name+" (recordings kept)")
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	return nil
}

// MARK: - Live video proxy

// handleLiveProxy checks the request and hands it to the recording server's
// video plane (which proxies MediaMTX's HLS, query included: MediaMTX >= 1.21
// ties HLS requests together with ?session=).
func (s *Server) handleLiveProxy(w http.ResponseWriter, r *http.Request) {
	id, file := r.PathValue("id"), r.PathValue("file")
	if !cameraIDPattern.MatchString(id) || !hlsFilePattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad stream path")
		return
	}
	c, err := s.Store.Camera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	s.serveVideo(w, r, c.RecorderID)
}

// MARK: - Recordings

type segment struct {
	link.Segment
	URL        string `json:"url"`
	RecorderID string `json:"recorderId"` // the recording server that has the file
}

func (s *Server) handleListRecordings(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !cameraIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "bad camera id")
		return
	}
	out, err := s.listSegments(r.Context(), id)
	if err != nil {
		writeError(w, commandStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// listSegments returns a camera's recorded segments, newest first, from
// every recording server it has been on that is connected now (footage
// stays where it was recorded when a camera moves).
func (s *Server) listSegments(ctx context.Context, id string) ([]segment, error) {
	out := []segment{}
	var firstErr error
	asked := 0
	for _, rid := range s.footageRecorders(ctx, id, 0, 0, true) {
		if !s.recorderConnected(rid) {
			continue
		}
		var segs []link.Segment
		if err := s.command(ctx, rid, link.CmdRecordings, link.RecordingsArgs{CameraID: id}, &segs); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		asked++
		for _, g := range segs {
			out = append(out, segment{Segment: g, URL: "/recordings/" + id + "/" + g.Name, RecorderID: rid})
		}
	}
	if asked == 0 {
		if firstErr == nil {
			firstErr = link.ErrNoRecorder
		}
		return nil, firstErr
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start > out[j].Start })
	return out, nil
}

// segmentRecorder is the recording server that has a camera's segment
// file: the one that had the camera when the file started (its name is
// the start time, the recording server's local time).
func (s *Server) segmentRecorder(ctx context.Context, cameraID, file string) string {
	at := time.Now().Unix()
	if len(file) >= 15 {
		if t, err := time.ParseInLocation("20060102-150405", file[:15], time.Local); err == nil {
			at = t.Unix()
		}
	}
	return s.recorderAt(ctx, cameraID, at)
}

func (s *Server) handleRecordingFile(w http.ResponseWriter, r *http.Request) {
	s.serveRecording(w, r, r.PathValue("id"), r.PathValue("file"))
}

// serveRecording streams one segment from the recording server, with Range
// support so players can seek.
func (s *Server) serveRecording(w http.ResponseWriter, r *http.Request, id, file string) {
	if !cameraIDPattern.MatchString(id) || !segmentPattern.MatchString(file) {
		writeError(w, http.StatusBadRequest, "bad recording path")
		return
	}
	s.serveVideoPath(w, r, s.segmentRecorder(r.Context(), id, file), "/recordings/"+id+"/"+file, nil)
}

// MARK: - Settings & system

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(s.Store.Setting(r.Context(), "default_retention_days", defaultRetention))
	writeJSON(w, http.StatusOK, map[string]any{"defaultRetentionDays": days})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DefaultRetentionDays int `json:"defaultRetentionDays"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.DefaultRetentionDays < 1 || in.DefaultRetentionDays > 3650 {
		writeError(w, http.StatusBadRequest, "retention must be between 1 and 3650 days")
		return
	}
	old := s.Store.Setting(r.Context(), "default_retention_days", defaultRetention)
	if err := s.Store.SetSetting(r.Context(), "default_retention_days", strconv.Itoa(in.DefaultRetentionDays)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "Storage", "Changed default retention", old+" → "+strconv.Itoa(in.DefaultRetentionDays)+" days")
	if err := s.ApplyMedia(r.Context()); err != nil {
		slog.Error("media apply failed", "err", err)
	}
	s.handleGetSettings(w, r)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	rid, _ := s.defaultRecorderID(r.Context()) // v1's System page shows one server
	st, connected := s.statusOf(rid)
	if !connected {
		st.Media.Error = link.ErrNoRecorder.Error()
	}
	resp := map[string]any{
		"version":      s.Version,
		"os":           runtime.GOOS + "/" + runtime.GOARCH,
		"mediaRunning": st.Media.Running,
		"mediaError":   st.Media.Error,
	}
	var info link.StorageInfo
	if err := s.command(r.Context(), rid, link.CmdStorageInfo, nil, &info); err == nil {
		resp["recordingsDir"] = info.RecordingsDir
		resp["recordingsBytes"] = info.RecordingsBytes
		if info.HasDisk {
			resp["diskFreeBytes"] = info.DiskFreeBytes
			resp["diskTotalBytes"] = info.DiskTotalBytes
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
