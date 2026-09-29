package filetransfer

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const (
	fileID1 = "01890f6e-74e8-4c72-8f9c-61fca85b71e7"
	fileID2 = "11890f6e-74e8-4c72-8f9c-61fca85b71e7"
)

func authHeader(raw []byte) string {
	return "Bearer " + base64.RawURLEncoding.EncodeToString(raw)
}

func newFileServer(t *testing.T, s *Store) (*httptest.Server, *http.Client) {
	t.Helper()
	h := NewHandler(s, HandlerConfig{AllowedOrigins: map[string]struct{}{"https://chat.local": {}}})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, srv.Client()
}

func doRequest(t *testing.T, client *http.Client, method, url string, body io.Reader, auth string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func uploadURL(srv *httptest.Server, fileID string) string {
	return srv.URL + "/api/file/upload?fileId=" + fileID
}

func mustUpload(t *testing.T, s *Store, client *http.Client, srv *httptest.Server, fileID, room, conn string, body []byte) {
	t.Helper()
	token, _, err := s.MintToken(conn, room)
	if err != nil {
		t.Fatal(err)
	}
	res := doRequest(t, client, http.MethodPost, uploadURL(srv, fileID), bytes.NewReader(body), authHeader(token))
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload status %d", res.StatusCode)
	}
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	srv, client := newFileServer(t, s)

	framed := bytes.Repeat(frame(bytes.Repeat([]byte{0xA7}, 300)), 3)
	mustUpload(t, s, client, srv, fileID1, "0001", "conn1", framed)

	dlToken, _, err := s.MintToken("conn2", "0001")
	if err != nil {
		t.Fatal(err)
	}
	res := doRequest(t, client, http.MethodGet, srv.URL+"/api/file/"+fileID1, nil, authHeader(dlToken))
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download status %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("content type %q", got)
	}
	if res.ContentLength != int64(len(framed)) {
		t.Fatalf("content length %d want %d", res.ContentLength, len(framed))
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, framed) {
		t.Fatal("download delivered wrong bytes")
	}

	// The download token is single-use.
	res2 := doRequest(t, client, http.MethodGet, srv.URL+"/api/file/"+fileID1, nil, authHeader(dlToken))
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused token status %d", res2.StatusCode)
	}
}

func TestUploadAuthAndValidation(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	srv, client := newFileServer(t, s)

	for _, tc := range []struct {
		name   string
		auth   string
		fileID string
		want   int
	}{
		{"missing token", "", fileID1, http.StatusUnauthorized},
		{"malformed token", "Bearer %%%%", fileID1, http.StatusUnauthorized},
		{"short token", "Bearer " + base64.RawURLEncoding.EncodeToString(make([]byte, 31)), fileID1, http.StatusUnauthorized},
		{"bad scheme", "Basic " + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), fileID1, http.StatusUnauthorized},
		{"bad file id", "", "not-a-uuid", http.StatusBadRequest},
	} {
		res := doRequest(t, client, http.MethodPost, uploadURL(srv, tc.fileID), bytes.NewReader(frame(bytes.Repeat([]byte{1}, 32))), tc.auth)
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("%s: status %d want %d", tc.name, res.StatusCode, tc.want)
		}
	}
	if s.FileCount() != 0 {
		t.Fatal("rejected upload left a file behind")
	}
}

func TestUploadDuplicateID(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	srv, client := newFileServer(t, s)

	body := frame(bytes.Repeat([]byte{2}, 64))
	mustUpload(t, s, client, srv, fileID1, "0001", "conn1", body)

	token, _, _ := s.MintToken("conn2", "0001")
	res := doRequest(t, client, http.MethodPost, uploadURL(srv, fileID1), bytes.NewReader(body), authHeader(token))
	res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate status %d", res.StatusCode)
	}
}

func TestUploadAbortsOnBadStream(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	srv, client := newFileServer(t, s)

	// Bad frame length in the middle of the stream.
	badHeader := make([]byte, ChunkHeaderSize)
	bad := append(frame(bytes.Repeat([]byte{3}, 64)), badHeader...)
	res := doRequest(t, client, http.MethodPost, uploadURL(srv, fileID1), bytes.NewReader(bad), freshAuth(t, s))
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad frame status %d", res.StatusCode)
	}
	if s.FileCount() != 0 {
		t.Fatal("aborted upload not reclaimed")
	}

	// Truncated frame.
	truncated := append(frame(bytes.Repeat([]byte{4}, 64)), 1, 2, 3, 4)
	res2 := doRequest(t, client, http.MethodPost, uploadURL(srv, fileID2), bytes.NewReader(truncated), freshAuth(t, s))
	res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("truncated status %d", res2.StatusCode)
	}
	if s.FileCount() != 0 {
		t.Fatal("aborted upload not reclaimed")
	}
}

// freshAuth mints a one-shot upload credential for room 0001; tokens are
// consumed on use, so one shared connection id never hits the mint cap.
func freshAuth(t *testing.T, s *Store) string {
	t.Helper()
	token, _, err := s.MintToken("auth-conn", "0001")
	if err != nil {
		t.Fatal(err)
	}
	return authHeader(token)
}

func TestUploadClientDisconnectAborts(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	srv, client := newFileServer(t, s)

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, uploadURL(srv, fileID1), pr)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.MintToken("connX", "0001")
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", authHeader(token))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	go func() { _, _ = client.Do(req) }()

	if _, err := pw.Write(frame(bytes.Repeat([]byte{5}, 128))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "upload to register", func() bool { return s.FileCount() == 1 })

	cancel()
	waitFor(t, "disconnect abort", func() bool { return s.FileCount() == 0 })
	_ = pw.CloseWithError(io.ErrClosedPipe)
}

func TestDownloadErrors(t *testing.T) {
	clock := newFakeClock()
	var expired int
	var mu sync.Mutex
	s := testStore(clock, func(string, string) { mu.Lock(); expired++; mu.Unlock() })
	srv, client := newFileServer(t, s)

	// No token.
	res := doRequest(t, client, http.MethodGet, srv.URL+"/api/file/"+fileID1, nil, "")
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token status %d", res.StatusCode)
	}

	// Unknown file.
	token, _, _ := s.MintToken("connA", "0001")
	res2 := doRequest(t, client, http.MethodGet, srv.URL+"/api/file/"+fileID1, nil, authHeader(token))
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown file status %d", res2.StatusCode)
	}

	mustUpload(t, s, client, srv, fileID1, "0001", "connB", frame(bytes.Repeat([]byte{6}, 64)))

	// Token minted for a different room.
	crossToken, _, _ := s.MintToken("connC", "0002")
	res3 := doRequest(t, client, http.MethodGet, srv.URL+"/api/file/"+fileID1, nil, authHeader(crossToken))
	res3.Body.Close()
	if res3.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-room status %d", res3.StatusCode)
	}

	// Deferred expiry: hold one download open via the store so the TTL
	// defers, then the HTTP endpoint must answer 410 for new downloads.
	dlToken, _, _ := s.MintToken("connD", "0001")
	dl, err := s.OpenDownload(context.Background(), dlToken, fileID1)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Close()
	clock.Add(time.Second)
	waitFor(t, "expiry broadcast", func() bool { mu.Lock(); defer mu.Unlock(); return expired == 1 })
	httpToken, _, _ := s.MintToken("connE", "0001")
	res4 := doRequest(t, client, http.MethodGet, srv.URL+"/api/file/"+fileID1, nil, authHeader(httpToken))
	res4.Body.Close()
	if res4.StatusCode != http.StatusGone {
		t.Fatalf("deferred status %d", res4.StatusCode)
	}
}

func TestMethodsAndPaths(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	srv, client := newFileServer(t, s)

	for _, tc := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"get upload", http.MethodGet, "/api/file/upload", http.StatusMethodNotAllowed},
		{"post download", http.MethodPost, "/api/file/" + fileID1, http.StatusMethodNotAllowed},
		{"put upload", http.MethodPut, "/api/file/upload", http.StatusMethodNotAllowed},
		{"bare prefix", http.MethodGet, "/api/file/", http.StatusNotFound},
		{"nested path", http.MethodGet, "/api/file/" + fileID1 + "/x", http.StatusNotFound},
		{"unrelated", http.MethodGet, "/api/other", http.StatusNotFound},
	} {
		res := doRequest(t, client, tc.method, srv.URL+tc.path, nil, "")
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatalf("%s: status %d want %d", tc.name, res.StatusCode, tc.want)
		}
	}
}

func TestOriginCheck(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	h := NewHandler(s, HandlerConfig{AllowedOrigins: map[string]struct{}{"https://chat.local": {}}})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/file/"+fileID1, nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin status %d", res.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/file/upload?fileId="+fileID1, nil)
	req2.Header.Set("Origin", "https://chat.local")
	res2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("allowed origin rejected: %d", res2.StatusCode)
	}
}
