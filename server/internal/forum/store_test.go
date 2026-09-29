package forum

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func testStore(t *testing.T, maxPosts int) (*Store, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "forum.json")
	store, err := NewStore(path, maxPosts)
	if err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestStorePersistsOnlyNameAndMessage(t *testing.T) {
	store, path := testStore(t, 10)
	post := Post{Name: "匿名甲", Message: "这里可以更好。"}
	if err := store.Add(post); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || len(raw[0]) != 3 || raw[0]["name"] != post.Name || raw[0]["message"] != post.Message {
		t.Fatalf("unexpected persisted record: %#v", raw)
	}
	if replies, ok := raw[0]["replies"].([]any); !ok || len(replies) != 0 {
		t.Fatalf("unexpected persisted replies: %#v", raw[0]["replies"])
	}
	for _, forbidden := range []string{"id", "ip", "mac", "time", "timestamp", "publishedAt", "userAgent"} {
		if _, ok := raw[0][forbidden]; ok {
			t.Fatalf("persisted metadata field %q", forbidden)
		}
	}

	reloaded, err := NewStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.List(); len(got) != 1 || !reflect.DeepEqual(got[0], Post{Name: post.Name, Message: post.Message, Replies: []Reply{}}) {
		t.Fatalf("unexpected reloaded posts: %#v", got)
	}
}

func TestStorePersistsAndRestoresReplies(t *testing.T) {
	store, path := testStore(t, 10)
	if err := store.Add(Post{Name: "匿名甲", Message: "主留言"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddReply(0, Reply{Name: "匿名乙", Message: "回复内容"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	posts := reloaded.List()
	if len(posts) != 1 || len(posts[0].Replies) != 1 || posts[0].Replies[0].Message != "回复内容" {
		t.Fatalf("reply was not restored: %#v", posts)
	}
}

func TestStoreRejectsInvalidPost(t *testing.T) {
	store, _ := testStore(t, 10)
	for _, post := range []Post{
		{Name: "A", Message: "valid"},
		{Name: "匿名甲", Message: ""},
		{Name: "匿名\n甲", Message: "valid"},
		{Name: "匿名甲", Message: "bad\x00text"},
	} {
		if err := store.Add(post); !errors.Is(err, ErrInvalidPost) {
			t.Fatalf("post %#v: want ErrInvalidPost, got %v", post, err)
		}
	}
}

func TestStoreStopsAtConfiguredLimit(t *testing.T) {
	store, _ := testStore(t, 1)
	if err := store.Add(Post{Name: "匿名甲", Message: "第一条"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Post{Name: "匿名乙", Message: "第二条"}); !errors.Is(err, ErrFull) {
		t.Fatalf("want ErrFull, got %v", err)
	}
}

func TestStoreRejectsUnknownPersistedFields(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "forum.json")
	if err := os.WriteFile(path, []byte(`[{"name":"匿名甲","message":"吐槽","timestamp":"never"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path, 10); err == nil {
		t.Fatal("unknown persisted metadata field was accepted")
	}
}
