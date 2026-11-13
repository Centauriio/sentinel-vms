package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type usbTestTransport func(*http.Request) (*http.Response, error)

func (f usbTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const usbTestProfile = `{"format":"mjpeg","width":1920,"height":1080,"fps":30,"bitrate":4000}`

func TestUSBWebLifecycleAndSecretBoundary(t *testing.T) {
	s := newCompanionServer(t)
	admin := v2Token(t, s, "usb-admin", "Admin")
	operator := v2Token(t, s, "usb-operator", "Operator")
	instance := ""
	state := "stopped"
	requested := false
	deleted := false
	offline := false
	calls := 0
	s.usbClient = &http.Client{Transport: usbTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		w := httptest.NewRecorder()
		if offline {
			w.WriteHeader(503)
			_, _ = w.WriteString(`{"error":"secret-must-not-leak"}`)
			return w.Result(), nil
		}
		c := map[string]any{"instance": instance, "device": "windows:abc", "kind": "windows", "state": state, "requested": requested, "profile": json.RawMessage(usbTestProfile)}
		switch {
		case r.Method == "POST" && r.URL.Path == "/cameras":
			var body struct{ Instance string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			instance = body.Instance
			c["instance"] = instance
			c["url"] = "rtsp://reader:secret-must-not-leak@127.0.0.1:18555/camera"
		case strings.HasSuffix(r.URL.Path, "/start"):
			state = "running"
			requested = true
			c["state"] = state
			c["requested"] = true
		case strings.HasSuffix(r.URL.Path, "/stop"):
			state = "stopped"
			requested = false
			c["state"] = state
			c["requested"] = false
		case r.Method == "DELETE":
			deleted = true
		case r.URL.Path == "/inventory":
			c = map[string]any{"devices": []any{}, "cameras": []any{c}, "windowsOnline": true}
		}
		_ = json.NewEncoder(w).Encode(c)
		return w.Result(), nil
	})}
	h := s.Handler()
	for _, token := range []string{"", operator} {
		before := calls
		code, _ := v2Call(t, h, "POST", "/api/v2/usb-cameras", token, `{"name":"Desk","device":"windows:abc","profile":`+usbTestProfile+`}`)
		if code != 401 && code != 403 {
			t.Fatalf("unauthorized create: %d", code)
		}
		if calls != before {
			t.Fatal("unauthorized request reached broker")
		}
	}
	code, m := v2Call(t, h, "POST", "/api/v2/usb-cameras", admin, `{"name":"Desk","device":"windows:abc","profile":`+usbTestProfile+`}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, m)
	}
	encoded, _ := json.Marshal(m)
	if strings.Contains(string(encoded), "secret") || m["url"] != nil {
		t.Fatal("browser received credentials")
	}
	id := m["cameraId"].(string)
	cam, err := s.Store.Camera(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	pass, err := s.Store.CameraPassword(cam)
	if err != nil || pass != "secret-must-not-leak" || strings.Contains(cam.RTSPURL, "secret") {
		t.Fatalf("credential storage: %v", err)
	}
	for _, action := range []string{"start", "stop"} {
		code, m = v2Call(t, h, "POST", "/api/v2/usb-cameras/"+id+"/"+action, admin, `{}`)
		if code != 200 || m["requested"] != (action == "start") {
			t.Fatalf("%s: %d %v", action, code, m)
		}
	}
	code, m = v2Call(t, h, "PUT", "/api/v2/usb-cameras/"+id, admin, `{"profile":`+usbTestProfile+`}`)
	if code != 200 {
		t.Fatalf("quality: %d %v", code, m)
	}
	for _, body := range []string{`{"rtspUrl":"rtsp://other/camera"}`, `{"password":"other"}`} {
		code, m = v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, body)
		if code != 400 {
			t.Fatalf("managed source edit: %d %v", code, m)
		}
	}
	code, m = v2Call(t, h, "PATCH", "/api/v2/cameras/"+id, admin, `{"name":"Office"}`)
	if code != 200 {
		t.Fatalf("ordinary edit: %d %v", code, m)
	}
	offline = true
	code, m = v2Call(t, h, "GET", "/api/v2/usb-cameras", admin, "")
	if code != 200 || m["available"] != false || len(m["cameras"].([]any)) != 1 {
		t.Fatalf("offline list lost binding: %d %v", code, m)
	}
	code, m = v2Call(t, h, "DELETE", "/api/v2/cameras/"+id, admin, "")
	if code < 400 || strings.Contains(errMessage(m), "secret") {
		t.Fatalf("failed removal: %d %v", code, m)
	}
	if _, err = s.Store.Camera(context.Background(), id); err != nil {
		t.Fatal("deleted DB camera while capture cleanup failed")
	}
	offline = false
	code, m = v2Call(t, h, "DELETE", "/api/v2/cameras/"+id, admin, "")
	if code != 204 || !deleted {
		t.Fatalf("delete: %d %v", code, m)
	}
	bindings, err := s.Store.USBBindings(context.Background())
	if err != nil || len(bindings) != 0 {
		t.Fatal("USB binding did not cascade")
	}
}

func TestUSBCancelledCreationCleansBroker(t *testing.T) {
	s := newCompanionServer(t)
	admin := v2Token(t, s, "usb-admin", "Admin")
	cleanup := false
	s.usbClient = &http.Client{Transport: usbTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" {
			cleanup = true
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		// Invalid broker connection must never create an unusable VMS record.
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"url":"invalid"}`))}, nil
	})}
	code, _ := v2Call(t, s.Handler(), "POST", "/api/v2/usb-cameras", admin, `{"name":"Desk","device":"windows:abc","profile":`+usbTestProfile+`}`)
	cams, _ := s.Store.Cameras(context.Background())
	if code != 503 || !cleanup || len(cams) != 0 {
		t.Fatalf("rollback: status=%d cleanup=%v cameras=%d", code, cleanup, len(cams))
	}
}
