package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/auth"
	"sentinel-linux/internal/store"
)

type usbProfile struct {
	Format  string  `json:"format"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	FPS     float64 `json:"fps"`
	Bitrate int     `json:"bitrate"`
}
type usbDevice struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Kind  string       `json:"kind"`
	Modes []usbProfile `json:"modes"`
}
type usbCamera struct {
	CameraID  string     `json:"cameraId,omitempty"`
	Instance  string     `json:"instance"`
	Device    string     `json:"device"`
	Kind      string     `json:"kind"`
	Profile   usbProfile `json:"profile"`
	State     string     `json:"state"`
	Requested bool       `json:"requested"`
}
type usbInventory struct {
	Available     bool        `json:"available"`
	Message       string      `json:"message,omitempty"`
	Devices       []usbDevice `json:"devices"`
	Cameras       []usbCamera `json:"cameras"`
	WindowsOnline bool        `json:"windowsOnline"`
}

var localUSBClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
	DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/run/sentinel-usb-control/control.sock")
	},
}}

// Only this server sees broker reader credentials. Errors never echo response bodies.
func (s *Server) usbCall(ctx context.Context, method, path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if in == nil {
		data = []byte("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://usb"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.usbClient
	if client == nil {
		client = localUSBClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("USB helper unavailable. Check that the USB add-on is installed and running on this server.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusBadRequest {
			return inputError{"Camera unavailable or quality settings unsupported. Refresh the camera list and try again."}
		}
		return errors.New("USB operation failed. Check the camera helper and try again.")
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(out); err != nil {
			return errors.New("Invalid USB helper response.")
		}
	}
	return nil
}

func (s *Server) registerV2USB(mux *http.ServeMux) {
	s.v2Route(mux, "GET /api/v2/usb-cameras", auth.ManageCameras, s.handleUSBList)
	s.v2Route(mux, "POST /api/v2/usb-cameras", auth.ManageCameras, s.handleUSBCreate)
	s.v2Route(mux, "PUT /api/v2/usb-cameras/{cameraId}", auth.ManageCameras, s.inScope("cameraId", s.handleUSBUpdate))
	s.v2Route(mux, "POST /api/v2/usb-cameras/{cameraId}/start", auth.ManageCameras, s.inScope("cameraId", s.handleUSBStart))
	s.v2Route(mux, "POST /api/v2/usb-cameras/{cameraId}/stop", auth.ManageCameras, s.inScope("cameraId", s.handleUSBStop))
}

func (s *Server) handleUSBList(w http.ResponseWriter, r *http.Request) {
	if !s.cameraScope(r.Context()).all {
		writeV2Error(w, 403, codeForbidden, "USB device setup requires access to all cameras.")
		return
	}
	bindings, err := s.Store.USBBindings(r.Context())
	if err != nil {
		writeV2Fail(w, err, "USB cameras")
		return
	}
	result := usbInventory{Devices: []usbDevice{}, Cameras: []usbCamera{}}
	err = s.usbCall(r.Context(), "GET", "/inventory", nil, &result)
	if err != nil {
		result.Message = "USB helper unavailable. Install or repair the USB add-on on this server."
	} else {
		result.Available = true
	}
	byInstance := map[string]usbCamera{}
	for _, c := range result.Cameras {
		byInstance[c.Instance] = c
	}
	result.Cameras = []usbCamera{}
	for id, instance := range bindings {
		c, ok := byInstance[instance]
		if !ok {
			c = usbCamera{Instance: instance, State: "helper-offline"}
		}
		c.CameraID = id
		result.Cameras = append(result.Cameras, c)
	}
	if result.Devices == nil {
		result.Devices = []usbDevice{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUSBCreate(w http.ResponseWriter, r *http.Request) {
	if !s.cameraScope(r.Context()).all {
		writeV2Error(w, 403, codeForbidden, "USB device setup requires access to all cameras.")
		return
	}
	var in struct {
		Name    string     `json:"name"`
		Device  string     `json:"device"`
		Profile usbProfile `json:"profile"`
	}
	if !readV2JSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 200 || in.Device == "" {
		writeV2Error(w, 400, codeBadRequest, "Choose a camera and enter its name.")
		return
	}
	if !s.recorderConnected(store.LocalRecorderID) {
		writeV2Error(w, 503, codeUnavailable, "USB cameras require a connected local recording server.")
		return
	}
	s.usbMu.Lock()
	defer s.usbMu.Unlock()
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeV2Fail(w, err, "USB camera")
		return
	}
	instance := "web-" + hex.EncodeToString(random[:])
	var created struct {
		usbCamera
		URL string `json:"url"`
	}
	committed := false
	defer func() {
		if !committed {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.usbCall(ctx, "DELETE", "/cameras/"+instance, nil, nil)
		}
	}()
	err := s.usbCall(r.Context(), "POST", "/cameras", map[string]any{"instance": instance, "device": in.Device, "profile": in.Profile}, &created)
	if err != nil {
		writeV2Fail(w, err, "USB camera")
		return
	}
	no := false
	ci := store.CameraInput{Name: in.Name, RTSPURL: created.URL, Recording: true, RecorderID: store.LocalRecorderID, CameraAnalytics: &no}
	if err = normalizeCamera(&ci); err != nil {
		writeV2Error(w, 503, codeUnavailable, "Invalid camera connection from USB helper.")
		return
	}
	cam, err := s.Store.SaveCamera(r.Context(), "", ci)
	if err != nil {
		writeV2Fail(w, err, "USB camera")
		return
	}
	if err = s.Store.BindUSB(r.Context(), cam.ID, instance); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Store.DeleteCamera(ctx, cam.ID)
		writeV2Fail(w, err, "USB camera")
		return
	}
	committed = true
	s.audit(r, "Cameras", "Added USB camera", cam.Name)
	_ = s.ApplyMedia(r.Context())
	created.usbCamera.CameraID = cam.ID
	writeJSON(w, http.StatusCreated, created.usbCamera)
}

func (s *Server) usbChange(w http.ResponseWriter, r *http.Request, action string) {
	s.usbMu.Lock()
	defer s.usbMu.Unlock()
	id := r.PathValue("cameraId")
	instance, err := s.Store.USBInstance(r.Context(), id)
	if err != nil {
		writeV2Fail(w, err, "USB camera")
		return
	}
	if instance == "" {
		writeV2Error(w, 404, codeNotFound, "USB camera not found.")
		return
	}
	path := "/cameras/" + instance
	method := "POST"
	var body any
	if action == "quality" {
		var in struct {
			Profile usbProfile `json:"profile"`
		}
		if !readV2JSON(w, r, &in) {
			return
		}
		body = in
		method = "PUT"
	} else {
		var in struct{}
		if !readV2JSON(w, r, &in) {
			return
		}
		path += "/" + action
	}
	var result usbCamera
	if err = s.usbCall(r.Context(), method, path, body, &result); err != nil {
		writeV2Fail(w, err, "USB camera")
		return
	}
	result.CameraID = id
	s.audit(r, "Cameras", "USB camera "+action, id)
	writeJSON(w, 200, result)
}
func (s *Server) handleUSBUpdate(w http.ResponseWriter, r *http.Request) {
	s.usbChange(w, r, "quality")
}
func (s *Server) handleUSBStart(w http.ResponseWriter, r *http.Request) { s.usbChange(w, r, "start") }
func (s *Server) handleUSBStop(w http.ResponseWriter, r *http.Request)  { s.usbChange(w, r, "stop") }
