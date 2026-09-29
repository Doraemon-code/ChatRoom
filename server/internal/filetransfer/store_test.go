package filetransfer

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock advances only when the test moves it, so timer tests never depend
// on real sleeps beyond the polling bound.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock           { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }
func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func testStore(clock *fakeClock, onExpired ExpiredFunc) *Store {
	return NewStore(Config{
		OnExpired:    onExpired,
		TTL:          50 * time.Millisecond,
		TokenTTL:     50 * time.Millisecond,
		UploadWindow: 50 * time.Millisecond,
		DeferralCap:  50 * time.Millisecond,
		Now:          clock.Now,
	})
}

func frame(body []byte) []byte {
	out := make([]byte, 0, ChunkHeaderSize+len(body))
	out = append(out, make([]byte, 12)...)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(body)))
	out = append(out, length[:]...)
	return append(out, body...)
}

func mustBegin(t *testing.T, s *Store, id, room string) *Upload {
	t.Helper()
	u, err := s.Begin(id, room)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func uploadBytes(t *testing.T, s *Store, id, room string, frames ...[]byte) *Upload {
	t.Helper()
	u := mustBegin(t, s, id, room)
	for _, f := range frames {
		if err := u.ReadChunk(bytes.NewReader(f)); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTokenLifecycle(t *testing.T) {
	clock := newFakeClock()
	s := testStore(clock, nil)
	raw, _, err := s.MintToken("conn1", "0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 32 {
		t.Fatalf("token length %d", len(raw))
	}
	room, err := s.authorize(bytes.Clone(raw))
	if err != nil || room != "0001" {
		t.Fatalf("authorize: %v %q", err, room)
	}
	// Single use: the same bytes fail the second time.
	if _, err := s.authorize(bytes.Clone(raw)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second use accepted: %v", err)
	}
	// Expiry.
	raw2, _, _ := s.MintToken("conn1", "0001")
	clock.Add(time.Second)
	if _, err := s.authorize(bytes.Clone(raw2)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token accepted: %v", err)
	}
	// Per-connection cap.
	s2 := NewStore(Config{TokenTTL: time.Minute, Now: clock.Now})
	for i := 0; i < MaxTokensPerConn; i++ {
		if _, _, err := s2.MintToken("connX", "0002"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s2.MintToken("connX", "0002"); !errors.Is(err, ErrTooManyTokens) {
		t.Fatalf("token cap not enforced: %v", err)
	}
	// Revocation.
	raw3, _, _ := s2.MintToken("connY", "0002")
	s2.RevokeConnection("connY")
	if _, err := s2.authorize(bytes.Clone(raw3)); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoked token accepted")
	}
}

func TestBeginRejectsDuplicateID(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	mustBegin(t, s, "a", "0001")
	if _, err := s.Begin("a", "0001"); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate accepted: %v", err)
	}
}

func TestReadChunkRoundTripAndValidation(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	big := bytes.Repeat([]byte{0xAB}, MaxChunkBody)
	u := uploadBytes(t, s, "f", "0001", frame([]byte("hello ciphertext16")), frame(big))
	e := u.entry
	if len(e.chunks) != 2 || e.size != int64(len(e.chunks[0])+len(e.chunks[1])) {
		t.Fatalf("chunk bookkeeping wrong: %d chunks size %d", len(e.chunks), e.size)
	}
	if !bytes.Equal(e.chunks[0][ChunkHeaderSize:], []byte("hello ciphertext16")) {
		t.Fatal("chunk not stored verbatim")
	}
	// Clean EOF at frame boundary.
	if err := u.ReadChunk(bytes.NewReader(nil)); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	// Truncated frame.
	u2 := mustBegin(t, s, "g", "0001")
	if err := u2.ReadChunk(io.MultiReader(bytes.NewReader(frame([]byte("x"))), bytes.NewReader([]byte{1, 2}))); err == nil {
		t.Fatal("truncated stream accepted")
	}
	// Invalid lengths.
	for i, length := range []uint32{0, MinChunkBody - 1, MaxChunkBody + MinChunkBody + 1} {
		head := make([]byte, ChunkHeaderSize)
		binary.BigEndian.PutUint32(head[12:16], length)
		u3 := mustBegin(t, s, string(rune('h'+i)), "0001")
		if err := u3.ReadChunk(bytes.NewReader(head)); err != ErrBadFrame {
			t.Fatalf("length %d accepted: %v", length, err)
		}
	}
}

// The hard requirement: every wipe path must leave the chunk buffers zeroed
// and no references behind.
func TestAbortWipesBuffersToZero(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	u := uploadBytes(t, s, "f", "0001", frame(bytes.Repeat([]byte{0x5A}, 64)), frame(bytes.Repeat([]byte{0xC3}, 64)))
	// Capture the actual chunk slices (not clones): after the wipe the entry
	// drops its references, but these slice headers still view the backing
	// arrays, which must read back as all zeros.
	aliases := [][]byte{u.entry.chunks[0], u.entry.chunks[1]}
	s.Abort(u)
	s.Abort(u) // idempotent
	if s.FileCount() != 0 {
		t.Fatal("file not removed from map")
	}
	if u.entry.chunks != nil {
		t.Fatal("chunk references not dropped")
	}
	for i, alias := range aliases {
		for j, b := range alias {
			if b != 0 {
				t.Fatalf("chunk %d byte %d not zeroed: %x", i, j, b)
			}
		}
	}
}

func TestExpiryWipesAndNotifiesOnce(t *testing.T) {
	clock := newFakeClock()
	var expired []string
	var mu sync.Mutex
	s := testStore(clock, func(roomID, fileID string) {
		mu.Lock()
		expired = append(expired, roomID+":"+fileID)
		mu.Unlock()
	})
	u := uploadBytes(t, s, "f", "0003", frame(bytes.Repeat([]byte{0x77}, 32)))
	alias := u.entry.chunks[0]
	if _, err := s.Complete(u); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second) // past TTL
	waitFor(t, "expiry", func() bool { return s.FileCount() == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(expired) != 1 || expired[0] != "0003:f" {
		t.Fatalf("OnExpired calls: %v", expired)
	}
	for i, b := range alias {
		if b != 0 {
			t.Fatalf("chunk byte %d not zeroed after expiry: %x", i, b)
		}
	}
}

func TestExpiryDefersToActiveDownload(t *testing.T) {
	clock := newFakeClock()
	var expiredCount int
	var mu sync.Mutex
	s := testStore(clock, func(roomID, fileID string) { mu.Lock(); expiredCount++; mu.Unlock() })

	payload := frame(bytes.Repeat([]byte{0x11}, 4096))
	u := uploadBytes(t, s, "f", "0001", payload, payload, payload)
	if _, err := s.Complete(u); err != nil {
		t.Fatal(err)
	}

	// A download whose writer blocks until the test releases it.
	token, _, _ := s.MintToken("conn1", "0001")
	release := make(chan struct{})
	started := make(chan struct{})
	dl, err := s.OpenDownload(context.Background(), bytes.Clone(token), "f")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var received bytes.Buffer
		err := dl.StreamTo(&blockUntilWriter{release: release, started: started, inner: &received, blockAfter: 2, blockTimeout: 750 * time.Millisecond})
		if err == nil {
			// The deferred download must still deliver every frame verbatim.
			want := bytes.Repeat(payload, 3)
			if !bytes.Equal(received.Bytes(), want) {
				err = errors.New("deferred download delivered wrong bytes")
			}
		}
		done <- err
	}()
	<-started

	clock.Add(time.Second) // TTL fires while the download is mid-stream
	waitFor(t, "file_expired broadcast", func() bool { mu.Lock(); defer mu.Unlock(); return expiredCount == 1 })
	if s.FileCount() != 1 {
		t.Fatal("deferred file wiped too early")
	}
	// New downloads are rejected in the deferred state.
	token2, _, _ := s.MintToken("conn1", "0001")
	if _, err := s.OpenDownload(context.Background(), bytes.Clone(token2), "f"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("deferred file still downloadable: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("deferred download failed: %v", err)
	}
	dl.Close()
	waitFor(t, "post-download wipe", func() bool { return s.FileCount() == 0 })
	mu.Lock()
	defer mu.Unlock()
	if expiredCount != 1 {
		t.Fatalf("OnExpired fired %d times", expiredCount)
	}
}

// blockUntilWriter lets the first blockAfter writes through, then blocks
// until release closes or the per-block timeout elapses — mirroring the
// production write deadline, which always returns the entry lock to expiry
// timers within a bounded window. Pointer receiver: the write counter must
// persist across calls.
type blockUntilWriter struct {
	release      <-chan struct{}
	started      chan<- struct{}
	inner        io.Writer
	blockAfter   int
	blockTimeout time.Duration
	writes       int
}

func (b *blockUntilWriter) Write(p []byte) (int, error) {
	b.writes++
	if b.writes > b.blockAfter {
		select {
		case b.started <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-time.After(b.blockTimeout):
		}
	}
	return b.inner.Write(p)
}

func TestDeferralHardCapForceWipes(t *testing.T) {
	clock := newFakeClock()
	s := testStore(clock, nil)
	payload := frame(bytes.Repeat([]byte{0x22}, 1024))
	u := uploadBytes(t, s, "f", "0001", payload)
	alias := u.entry.chunks[0]
	if _, err := s.Complete(u); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	defer close(release)
	token, _, _ := s.MintToken("conn1", "0001")
	dl, err := s.OpenDownload(context.Background(), bytes.Clone(token), "f")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = dl.StreamTo(&blockUntilWriter{release: release, inner: io.Discard, blockAfter: 0, blockTimeout: 750 * time.Millisecond})
	}()

	clock.Add(time.Second) // TTL fires (deferred)…
	waitFor(t, "deferral", func() bool { return s.FileCount() == 1 })
	clock.Add(time.Second) // …then the deferral cap fires.
	waitFor(t, "hard-cap wipe", func() bool { return s.FileCount() == 0 })
	for i, b := range alias {
		if b != 0 {
			t.Fatalf("chunk byte %d not zeroed after hard cap: %x", i, b)
		}
	}
}

func TestUploadWindowAbortsStalledUpload(t *testing.T) {
	clock := newFakeClock()
	var expired int
	var mu sync.Mutex
	s := testStore(clock, func(roomID, fileID string) { mu.Lock(); expired++; mu.Unlock() })
	u := uploadBytes(t, s, "f", "0001", frame(bytes.Repeat([]byte{0x33}, 128)))
	alias := u.entry.chunks[0]
	clock.Add(time.Second)
	waitFor(t, "upload-window abort", func() bool { return s.FileCount() == 0 })
	for i, b := range alias {
		if b != 0 {
			t.Fatalf("chunk byte %d not zeroed after upload window: %x", i, b)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if expired != 0 {
		t.Fatal("OnExpired fired for an aborted upload")
	}
}

func TestWipeRoomAndTokenPurge(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	uA1 := uploadBytes(t, s, "f1", "0001", frame([]byte("aaaaaaaaaaaaaaaa")))
	uA2 := uploadBytes(t, s, "f2", "0001", frame([]byte("bbbbbbbbbbbbbbbb")))
	uB := uploadBytes(t, s, "f3", "0002", frame([]byte("cccccccccccccccc")))
	aliases := [][]byte{uA1.entry.chunks[0], uA2.entry.chunks[0]}
	s.MintToken("conn1", "0001")
	s.MintToken("conn2", "0002")
	s.WipeRoom("0001")
	if s.FileCount() != 1 {
		t.Fatal("room wipe removed the wrong files")
	}
	for _, alias := range aliases {
		for i, b := range alias {
			if b != 0 {
				t.Fatalf("room-wipe left byte %d = %x", i, b)
			}
		}
	}
	// Room 0002's file and tokens survive.
	if _, err := s.Complete(uB); err != nil {
		t.Fatal(err)
	}
	if s.TokenCount() != 1 {
		t.Fatalf("room token not purged: %d left", s.TokenCount())
	}
}

func TestAuthorizeRoomBinding(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	u := uploadBytes(t, s, "f", "0002", frame([]byte("dddddddddddddddd")))
	if _, err := s.Complete(u); err != nil {
		t.Fatal(err)
	}
	token, _, _ := s.MintToken("conn1", "0001") // minted for a different room
	if _, err := s.OpenDownload(context.Background(), bytes.Clone(token), "f"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("cross-room download allowed: %v", err)
	}
}

func TestWipeAllCancelsActiveDownloads(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	u := uploadBytes(t, s, "f", "0001", frame([]byte("eeeeeeeeeeeeeeee")))
	if _, err := s.Complete(u); err != nil {
		t.Fatal(err)
	}
	token, _, _ := s.MintToken("conn1", "0001")
	dl, err := s.OpenDownload(context.Background(), bytes.Clone(token), "f")
	if err != nil {
		t.Fatal(err)
	}
	s.WipeAll()
	select {
	case <-dl.ctx.Done():
	default:
		t.Fatal("active download context not cancelled by WipeAll")
	}
	dl.Close()
}

func TestCompleteAfterWipeFails(t *testing.T) {
	s := testStore(newFakeClock(), nil)
	u := uploadBytes(t, s, "f", "0001", frame([]byte("ffffffffffffffff")))
	s.Abort(u)
	if _, err := s.Complete(u); !errors.Is(err, ErrWiped) {
		t.Fatalf("Complete after wipe: %v", err)
	}
}

func TestErrorsCarryNoPayload(t *testing.T) {
	// Belt-and-braces for the no-residue promise: no error text ever embeds
	// stored bytes, file contents, or token material.
	s := testStore(newFakeClock(), nil)
	secret := []byte("top secret frame body")
	u := uploadBytes(t, s, "f", "0001", frame(secret))
	s.Abort(u)
	for _, err := range []error{ErrInvalidToken, ErrNotAuthorized, ErrDuplicateID, ErrNotFound, ErrNotReady, ErrTooManyTokens, ErrWiped, ErrBadFrame, ErrWrongState} {
		if strings.Contains(err.Error(), "top secret") {
			t.Fatal("error message leaks payload")
		}
	}
}
