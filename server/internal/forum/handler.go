package forum

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Config struct {
	AllowedOrigins map[string]struct{}
	MaxBodyBytes   int64
}

type Handler struct {
	store  *Store
	config Config
}

type messageInput struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

func NewHandler(store *Store, config Config) *Handler {
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = MaxRequestBytes
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
	if r.URL.Path == "/api/board/posts" {
		switch r.Method {
		case http.MethodGet:
			h.list(w)
		case http.MethodPost:
			h.create(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed)
		}
		return
	}

	const prefix = "/api/board/posts/"
	if strings.HasPrefix(r.URL.Path, prefix) && strings.HasSuffix(r.URL.Path, "/replies") && r.Method == http.MethodPost {
		indexText := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/replies")
		index, err := strconv.Atoi(indexText)
		if err != nil || index < 0 || strconv.Itoa(index) != indexText {
			writeError(w, http.StatusNotFound)
			return
		}
		h.reply(w, r, index)
		return
	}
	writeError(w, http.StatusNotFound)
}

func (h *Handler) list(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, h.store.List())
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var input messageInput
	if !h.decode(w, r, &input) {
		return
	}
	post := Post{Name: input.Name, Message: input.Message, Replies: []Reply{}}
	if err := validatePost(post); err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	if err := h.store.Add(post); err != nil {
		if errors.Is(err, ErrFull) {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, ErrInvalidPost) {
			writeError(w, http.StatusBadRequest)
			return
		}
		writeError(w, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, post)
}

func (h *Handler) reply(w http.ResponseWriter, r *http.Request, index int) {
	var input messageInput
	if !h.decode(w, r, &input) {
		return
	}
	reply := Reply{Name: input.Name, Message: input.Message}
	if err := validateReply(reply); err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}
	if err := h.store.AddReply(index, reply); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound)
		case errors.Is(err, ErrRepliesFull):
			writeError(w, http.StatusServiceUnavailable)
		default:
			writeError(w, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusCreated, reply)
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, h.config.MaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}
