package whatsapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

// newFakeGraph returns a server standing in for the Graph API, plus a client
// pointed at it. Every test in this file runs entirely in memory.
func newFakeGraph(t *testing.T, handler http.HandlerFunc) (*whatsapp.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := whatsapp.NewClient(
		"PHONE_NUMBER_ID",
		"test-access-token",
		whatsapp.WithBaseURL(srv.URL),
		whatsapp.WithHTTPClient(srv.Client()),
	)
	return client, srv
}

func TestSendTextMessage_Success(t *testing.T) {
	var gotPath, gotAuth, gotBody string

	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.URL.Query().Get("access_token")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[{"id":"wamid.OUT1"}]}`))
	})

	err := client.SendTextMessage(context.Background(), "919812345678", "namaste papa")
	if err != nil {
		t.Fatalf("SendTextMessage() error = %v", err)
	}

	if gotPath != "/PHONE_NUMBER_ID/messages" {
		t.Errorf("path = %q, want /PHONE_NUMBER_ID/messages", gotPath)
	}
	if gotAuth != "test-access-token" {
		t.Errorf("access_token = %q", gotAuth)
	}

	var sent struct {
		MessagingProduct string `json:"messaging_product"`
		To               string `json:"to"`
		Type             string `json:"type"`
		Text             struct {
			Body string `json:"body"`
		} `json:"text"`
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.MessagingProduct != "whatsapp" {
		t.Errorf("messaging_product = %q", sent.MessagingProduct)
	}
	if sent.To != "919812345678" {
		t.Errorf("to = %q", sent.To)
	}
	if sent.Text.Body != "namaste papa" {
		t.Errorf("text.body = %q", sent.Text.Body)
	}
}

func TestSendTextMessage_RejectsEmptyInput(t *testing.T) {
	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server was called with invalid input")
	})

	tests := []struct {
		name string
		to   string
		body string
	}{
		{"empty recipient", "", "hi"},
		{"blank recipient", "   ", "hi"},
		{"empty body", "919812345678", ""},
		{"blank body", "919812345678", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := client.SendTextMessage(context.Background(), tt.to, tt.body); err == nil {
				t.Error("error = nil, want validation failure")
			}
		})
	}
}

func TestSendTextMessage_APIError(t *testing.T) {
	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid parameter","type":"OAuthException","code":100,"error_subcode":33,"error_data":{"details":"phone number not registered"}}}`))
	})

	err := client.SendTextMessage(context.Background(), "919812345678", "hi")
	if err == nil {
		t.Fatal("error = nil, want API error")
	}

	var apiErr *whatsapp.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *whatsapp.APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
	if apiErr.Code != 100 {
		t.Errorf("Code = %d, want 100", apiErr.Code)
	}
	if apiErr.Message != "Invalid parameter" {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

// A non-JSON error body must still produce a usable error, not a panic.
func TestSendTextMessage_NonJSONErrorBody(t *testing.T) {
	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>bad gateway</html>`))
	})

	err := client.SendTextMessage(context.Background(), "919812345678", "hi")
	if err == nil {
		t.Fatal("error = nil, want API error")
	}
	var apiErr *whatsapp.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *whatsapp.APIError", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d", apiErr.StatusCode)
	}
}

// The handler must never block on r.Context().Done(): Go's HTTP/1.1 server
// only cancels the request context when the connection actually closes, so a
// handler waiting on it deadlocks httptest.Server.Close(). An explicit release
// channel unblocks it deterministically.
func TestSendTextMessage_ContextCancellation(t *testing.T) {
	release := make(chan struct{})

	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := client.SendTextMessage(ctx, "919812345678", "hi"); err == nil {
		t.Error("error = nil, want a deadline error")
	}
}

func TestGetMediaURL_Success(t *testing.T) {
	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/MEDIA1" {
			t.Errorf("path = %q, want /MEDIA1", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"MEDIA1","mime_type":"audio/ogg; codecs=opus","file_size":1234,"url":"https://cdn.example/media/MEDIA1"}`))
	})

	url, err := client.GetMediaURL(context.Background(), "MEDIA1")
	if err != nil {
		t.Fatalf("GetMediaURL() error = %v", err)
	}
	if url != "https://cdn.example/media/MEDIA1" {
		t.Errorf("url = %q", url)
	}
}

func TestGetMediaURL_NoURLInResponse(t *testing.T) {
	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"MEDIA1","mime_type":"audio/ogg"}`))
	})

	_, err := client.GetMediaURL(context.Background(), "MEDIA1")
	if !errors.Is(err, whatsapp.ErrNoMediaURL) {
		t.Errorf("error = %v, want ErrNoMediaURL", err)
	}
}

func TestGetMediaURL_EmptyID(t *testing.T) {
	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server called with an empty media ID")
	})

	if _, err := client.GetMediaURL(context.Background(), ""); err == nil {
		t.Error("error = nil, want validation failure")
	}
}

func TestDownloadMedia_Success(t *testing.T) {
	// The CDN host is separate from the API host, mirroring reality.
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OGG-AUDIO-BYTES"))
	}))
	defer cdn.Close()

	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {})

	data, err := client.DownloadMedia(context.Background(), cdn.URL)
	if err != nil {
		t.Fatalf("DownloadMedia() error = %v", err)
	}
	if string(data) != "OGG-AUDIO-BYTES" {
		t.Errorf("data = %q", string(data))
	}
}

func TestDownloadMedia_HTTPError(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer cdn.Close()

	client, _ := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request) {})

	_, err := client.DownloadMedia(context.Background(), cdn.URL)
	if err == nil {
		t.Fatal("error = nil, want an error for a 404")
	}
	var apiErr *whatsapp.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *whatsapp.APIError", err)
	}
}
