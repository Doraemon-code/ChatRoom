package filetransfer

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lan-chat/server/internal/protocol"
)

// downloadWriteTimeout bounds one socket write during a download stream. A
// stalled reader must not pin the entry lock — and the ciphertext it guards —
// for longer than this; the write fails and the download ends.
const downloadWriteTimeout = 30 * time.Second

type HandlerConfig struct {
	AllowedOrigins map[string]struct{}
	// WriteTimeout bounds each download write; defaults to
	// downloadWriteTimeout.
	WriteTimeout time.Duration
}

// Handler serves the two file endpoints. POST /api/file/upload?fileId=UUID
// streams ciphertext frames in; GET /api/file/UUID streams them back out. Both
// demand a single-use bearer token minted over the WebSocket channel, and
// neither reads, logs, nor persists anything beyond the RAM store.
type Handler struct {
	store  *Store
	config HandlerConfig
}

func NewHandler(store *Store, config HandlerConfig) *Handler {
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = downloadWriteTimeout
	}
	return &Handler{store: store, config: config}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		if _, allowed := h.config.AllowedOrigins[origin]; !allowed {
			writeError(w, http.StatusForbidden)
			return
		}
	}
	switch {
	case r.URL.Path == "/api/file/upload":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed)
			return
		}
		h.upload(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/file/"):
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed)
			return
		}
		h.download(w, r)
	default:
		writeError(w, http.StatusNotFound)
	}
}

// bearerToken decodes the Authorization header: 32 raw bytes carried
// base64url, exactly as the file_token WebSocket frame delivered them.
func bearerToken(r *http.Request) ([]byte, bool) {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	return raw, true
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("fileId")
	if !protocol.ValidUUID(fileID) {
		writeError(w, http.StatusBadRequest)
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized)
		return
	}
	roomID, err := h.store.authorize(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized)
		return
	}
	u, err := h.store.Begin(fileID, roomID)
	if err != nil {
		if errors.Is(err, ErrDuplicateID) {
			writeError(w, http.StatusConflict)
			return
		}
		writeError(w, http.StatusInternalServerError)
		return
	}
	for {
		err := u.ReadChunk(r.Body)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Bad framing or a dead connection: reclaim the buffered
			// ciphertext immediately.
			h.store.Abort(u)
			switch {
			case errors.Is(err, ErrWiped):
				writeError(w, http.StatusGone)
			default:
				writeError(w, http.StatusBadRequest)
			}
			return
		}
		select {
		case <-r.Context().Done():
			h.store.Abort(u)
			return
		default:
		}
	}
	if _, err := h.store.Complete(u); err != nil {
		if errors.Is(err, ErrWiped) {
			writeError(w, http.StatusGone)
			return
		}
		writeError(w, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimPrefix(r.URL.Path, "/api/file/")
	if !protocol.ValidUUID(fileID) {
		writeError(w, http.StatusNotFound)
		return
	}
	token, ok := bearerToken(r)
	if !ok {
		writeError(w, http.StatusUnauthorized)
		return
	}
	dl, err := h.store.OpenDownload(r.Context(), token, fileID)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidToken):
			writeError(w, http.StatusUnauthorized)
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound)
		case errors.Is(err, ErrNotAuthorized):
			writeError(w, http.StatusForbidden)
		case errors.Is(err, ErrNotReady):
			writeError(w, http.StatusGone)
		default:
			writeError(w, http.StatusInternalServerError)
		}
		return
	}
	defer dl.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(dl.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if err := dl.StreamTo(&deadlineWriter{
		rc:      http.NewResponseController(w),
		w:       w,
		timeout: h.config.WriteTimeout,
	}); err != nil {
		// Mid-stream failure after headers were sent: the only move is to
		// end the response. The truncated ciphertext fails client-side GCM.
		return
	}
}

// deadlineWriter bounds each write so a stalled reader cannot hold the entry
// lock open indefinitely.
type deadlineWriter struct {
	rc      *http.ResponseController
	w       http.ResponseWriter
	timeout time.Duration
}

func (dw *deadlineWriter) Write(p []byte) (int, error) {
	// Best-effort: writers without deadline support (test doubles) still
	// work; the real net/http server always supports it.
	_ = dw.rc.SetWriteDeadline(time.Now().Add(dw.timeout))
	return dw.w.Write(p)
}

func writeError(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}
