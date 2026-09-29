package forum

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	MinNameRunes      = 2
	MaxNameRunes      = 24
	MaxMessageRunes   = 2000
	DefaultMaxPosts   = 10000
	MaxRepliesPerPost = 1000
	MaxRequestBytes   = 32 << 10
)

var (
	ErrInvalidPost = errors.New("invalid forum post")
	ErrFull        = errors.New("forum is full")
	ErrNotFound    = errors.New("forum post not found")
	ErrRepliesFull = errors.New("forum post replies are full")
)

// Post is intentionally the complete persisted forum thread. The only stored
// user data is each writer's name and message. Do not add an identifier,
// timestamp, client data, or request metadata to these types.
type Post struct {
	Name    string  `json:"name"`
	Message string  `json:"message"`
	Replies []Reply `json:"replies"`
}

type Reply struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	maxPosts int
	posts    []Post
}

func NewStore(path string, maxPosts int) (*Store, error) {
	if strings.TrimSpace(path) == "" || maxPosts <= 0 {
		return nil, errors.New("invalid forum store configuration")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	posts, err := readPosts(path)
	if err != nil {
		return nil, err
	}
	if len(posts) > maxPosts {
		return nil, ErrFull
	}
	return &Store{path: path, maxPosts: maxPosts, posts: posts}, nil
}

func (s *Store) List() []Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePosts(s.posts)
}

func (s *Store) Add(post Post) error {
	post.Replies = []Reply{}
	if err := validatePost(post); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.posts) >= s.maxPosts {
		return ErrFull
	}
	next := make([]Post, len(s.posts)+1)
	copy(next, s.posts)
	next[len(s.posts)] = post
	if err := writePosts(s.path, next); err != nil {
		return err
	}
	s.posts = next
	return nil
}

func (s *Store) AddReply(index int, reply Reply) error {
	if err := validateReply(reply); err != nil {
		return ErrInvalidPost
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.posts) {
		return ErrNotFound
	}
	if len(s.posts[index].Replies) >= MaxRepliesPerPost {
		return ErrRepliesFull
	}
	next := clonePosts(s.posts)
	next[index].Replies = append(next[index].Replies, reply)
	if err := writePosts(s.path, next); err != nil {
		return err
	}
	s.posts = next
	return nil
}

func readPosts(path string) ([]Post, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("empty forum store")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var posts []Post
	if err := decoder.Decode(&posts); err != nil || posts == nil {
		return nil, errors.New("invalid forum store")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid forum store")
	}
	for _, post := range posts {
		if post.Replies == nil {
			post.Replies = []Reply{}
		}
		if err := validatePost(post); err != nil {
			return nil, errors.New("invalid forum store")
		}
	}
	for index := range posts {
		if posts[index].Replies == nil {
			posts[index].Replies = []Reply{}
		}
	}
	return posts, nil
}

func writePosts(path string, posts []Post) error {
	data, err := json.Marshal(posts)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".forum-posts-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

func validatePost(post Post) error {
	if !validName(post.Name) || !validMessage(post.Message) || len(post.Replies) > MaxRepliesPerPost {
		return ErrInvalidPost
	}
	for _, reply := range post.Replies {
		if err := validateReply(reply); err != nil {
			return err
		}
	}
	return nil
}

func validateReply(reply Reply) error {
	if !validName(reply.Name) || !validMessage(reply.Message) {
		return ErrInvalidPost
	}
	return nil
}

func clonePosts(posts []Post) []Post {
	cloned := make([]Post, len(posts))
	for index, post := range posts {
		cloned[index] = post
		cloned[index].Replies = append([]Reply(nil), post.Replies...)
		if cloned[index].Replies == nil {
			cloned[index].Replies = []Reply{}
		}
	}
	return cloned
}

func validName(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	count := 0
	for _, r := range value {
		count++
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return count >= MinNameRunes && count <= MaxNameRunes
}

func validMessage(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	count := 0
	for _, r := range value {
		count++
		if (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return count > 0 && count <= MaxMessageRunes
}
