package forum

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandlerListsAndCreatesPostsWithoutMetadata(t *testing.T) {
	store, _ := testStore(t, 10)
	handler := NewHandler(store, Config{AllowedOrigins: map[string]struct{}{"https://chat.test": {}}})

	request := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewBufferString(`{"name":"匿名甲","message":"服务不错，但还可以更快。"}`))
	request.Header.Set("Origin", "https://chat.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", response.Code, response.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 {
		t.Fatalf("response contains metadata: %#v", created)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/board/posts", nil)
	request.Header.Set("Origin", "https://chat.test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected list response: %d %s", response.Code, response.Body.String())
	}
	var posts []Post
	if err := json.Unmarshal(response.Body.Bytes(), &posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Name != "匿名甲" {
		t.Fatalf("unexpected posts: %#v", posts)
	}
}

func TestHandlerRepliesToEachPost(t *testing.T) {
	store, _ := testStore(t, 10)
	handler := NewHandler(store, Config{})

	request := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewBufferString(`{"name":"匿名甲","message":"主留言"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create post: got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/board/posts/0/replies", bytes.NewBufferString(`{"name":"匿名乙","message":"回复主留言"}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create reply: got %d: %s", response.Code, response.Body.String())
	}
	posts := store.List()
	if len(posts) != 1 || len(posts[0].Replies) != 1 || posts[0].Replies[0].Name != "匿名乙" {
		t.Fatalf("reply was not attached: %#v", posts)
	}
}

func TestHandlerRejectsUnknownRequestFieldsAndOrigins(t *testing.T) {
	store, _ := testStore(t, 10)
	handler := NewHandler(store, Config{AllowedOrigins: map[string]struct{}{"https://chat.test": {}}})

	request := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewBufferString(`{"name":"匿名甲","message":"吐槽","ip":"1.2.3.4"}`))
	request.Header.Set("Origin", "https://chat.test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown metadata field accepted: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/board/posts", nil)
	request.Header.Set("Origin", "https://evil.test")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unexpected origin response: %d", response.Code)
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	store, _ := testStore(t, 10)
	handler := NewHandler(store, Config{MaxBodyBytes: 32})
	request := httptest.NewRequest(http.MethodPost, "/api/board/posts", bytes.NewBufferString(`{"name":"匿名甲","message":"这条内容会超过请求限制"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized request accepted: %d", response.Code)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
}

func TestStoreUsesPrivateFileMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "forum.json")
	store, err := NewStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Post{Name: "匿名甲", Message: "只保存必要字段"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("forum store mode = %o, want 600", info.Mode().Perm())
	}
}
