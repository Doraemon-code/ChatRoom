// Package filetransfer holds chat file ciphertext in RAM only, for a bounded
// lifetime, and wipes it in place when that lifetime ends. It never writes to
// disk, never logs, and never inspects the bytes it stores: the chunk framing
// is opaque relayed ciphertext produced and consumed by the clients.
package filetransfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	// TTL is the hard lifetime of a completed file: five minutes from upload
	// completion. A download already in progress when the TTL fires may run
	// to completion, bounded by DeferralCap.
	TTL = 5 * time.Minute
	// TokenTTL bounds a single-use transfer token.
	TokenTTL = 60 * time.Second
	// UploadWindow bounds how long an incomplete upload may hold RAM. An
	// unfinished upload is not yet a "sent file", so the five-minute product
	// rule does not apply to it, but it may not linger either.
	UploadWindow = 10 * time.Minute
	// DeferralCap is the extra time an expiry may be deferred behind an
	// active download before the download is force-aborted and wiped.
	DeferralCap = 5 * time.Minute
	// MaxTokensPerConn bounds simultaneously live tokens per connection.
	MaxTokensPerConn = 4

	// ChunkHeaderSize is one frame prefix: 12-byte IV + 4-byte big-endian
	// ciphertext length.
	ChunkHeaderSize = 16
	// MaxChunkBody is the largest ciphertext body one frame may carry
	// (1 MiB plaintext + 16-byte GCM tag).
	MaxChunkBody = 1 << 20
	// MaxChunkFrame is the largest complete frame.
	MaxChunkFrame = ChunkHeaderSize + MaxChunkBody + 16
	// MinChunkBody is the smallest valid ciphertext body (bare GCM tag).
	MinChunkBody = 16
)

var (
	ErrInvalidToken  = errors.New("file token invalid, expired, or already used")
	ErrNotAuthorized = errors.New("file token does not grant access to this file")
	ErrDuplicateID   = errors.New("file id already reserved")
	ErrNotFound      = errors.New("file not found")
	ErrNotReady      = errors.New("file not available for download")
	ErrTooManyTokens = errors.New("too many live tokens for connection")
	ErrWiped         = errors.New("file wiped during transfer")
	ErrBadFrame      = errors.New("invalid chunk framing")
	ErrWrongState    = errors.New("transfer handle in wrong state")
)

// ExpiredFunc runs once per stored file when its TTL fires, on a timer
// goroutine, with no store lock held. It is not invoked for aborted uploads,
// room-empty wipes, or shutdown wipes (nobody is left to notify).
type ExpiredFunc func(roomID, fileID string)

type Config struct {
	OnExpired ExpiredFunc
	// The following default to the package constants when zero; tests may
	// shorten them and inject a clock.
	TTL          time.Duration
	TokenTTL     time.Duration
	UploadWindow time.Duration
	DeferralCap  time.Duration
	Now          func() time.Time
}

// Store tracks in-RAM files and single-use transfer tokens.
//
// Locking discipline: Store.mu guards only the two maps; every entry has its
// own mutex guarding the file state. Code never holds Store.mu while
// acquiring an entry lock — a download blocked mid-chunk-write holds its
// entry lock for as long as one bounded socket write, and the store must keep
// serving other files meanwhile. Map removals therefore use removeEntry's
// identity check so a stale handle can never delete a newer entry that reuse
// of the same file ID created.
type Store struct {
	mu     sync.Mutex
	files  map[string]*entry
	tokens map[[32]byte]*tokenInfo
	cfg    Config

	ttl, tokenTTL, uploadWindow, deferralCap time.Duration
	now                                      func() time.Time
}

// The token registry is keyed by SHA-256(token): Go map deletes do not zero
// bucket memory, so the raw token — a live capability — is never stored; only
// its non-sensitive digest is.
type tokenInfo struct {
	connID    string
	roomID    string
	expiresAt time.Time
}

type fileState int

const (
	stateUploading       fileState = iota // reserved, body still streaming, not downloadable
	stateReady                            // complete, downloadable until TTL
	stateExpiredDeferred                  // TTL fired with downloads active; wipe deferred
)

type entry struct {
	mu        sync.Mutex
	id        string
	roomID    string
	state     fileState
	chunks    [][]byte // each chunk is one verbatim frame; buffers are never resized
	size      int64    // total framed bytes, served as download Content-Length
	timer     *time.Timer
	timerGen  uint64 // expires stale timer callbacks after re-arm
	downloads int
	nextDLID  uint64
	dls       []activeDownload
	wiped     bool
}

// activeDownload pairs a download's registration ID with its cancel func;
// func values cannot be compared in Go, so removal goes by ID.
type activeDownload struct {
	id     uint64
	cancel context.CancelFunc
}

// wipeLocked is the single place ciphertext is destroyed. It zeroes every
// chunk buffer in place, drops all references, stops the timer, and aborts any
// active downloads. Idempotent. Caller holds e.mu.
func (e *entry) wipeLocked() {
	if e.wiped {
		return
	}
	e.wiped = true
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	for _, chunk := range e.chunks {
		clear(chunk)
	}
	e.chunks = nil
	e.size = 0
	for _, dl := range e.dls {
		dl.cancel()
	}
	e.dls = nil
	e.downloads = 0
}

// armTimerLocked (re)arms the entry timer and bumps the generation so a
// callback from a previous timer — upload window, TTL, or deferral cap —
// becomes a no-op. Caller holds e.mu.
func (e *entry) armTimerLocked(s *Store, d time.Duration) {
	e.timerGen++
	gen := e.timerGen
	if e.timer != nil {
		e.timer.Stop()
	}
	fileID := e.id
	e.timer = time.AfterFunc(d, func() { s.expire(fileID, gen) })
}

// removeEntry deletes the map slot only if it still points at this exact
// entry, so a handle from before a wipe can never delete a newer entry that
// re-used the same file ID.
func (s *Store) removeEntry(fileID string, e *entry) {
	s.mu.Lock()
	if s.files[fileID] == e {
		delete(s.files, fileID)
	}
	s.mu.Unlock()
}

func NewStore(cfg Config) *Store {
	s := &Store{
		files:  make(map[string]*entry),
		tokens: make(map[[32]byte]*tokenInfo),
		cfg:    cfg,
	}
	s.ttl, s.tokenTTL, s.uploadWindow, s.deferralCap = cfg.TTL, cfg.TokenTTL, cfg.UploadWindow, cfg.DeferralCap
	if s.ttl <= 0 {
		s.ttl = TTL
	}
	if s.tokenTTL <= 0 {
		s.tokenTTL = TokenTTL
	}
	if s.uploadWindow <= 0 {
		s.uploadWindow = UploadWindow
	}
	if s.deferralCap <= 0 {
		s.deferralCap = DeferralCap
	}
	s.now = cfg.Now
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// MintToken creates a single-use token bound to (connID, roomID) and returns
// the raw 32 bytes exactly once; the store keeps only SHA-256(token). Expired
// tokens are purged on every mint.
func (s *Store) MintToken(connID, roomID string) ([]byte, time.Duration, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		clear(raw)
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	live := 0
	for key, info := range s.tokens {
		if !info.expiresAt.After(now) {
			delete(s.tokens, key)
			continue
		}
		if info.connID == connID {
			live++
		}
	}
	if live >= MaxTokensPerConn {
		clear(raw)
		return nil, 0, ErrTooManyTokens
	}
	s.tokens[sha256.Sum256(raw)] = &tokenInfo{connID: connID, roomID: roomID, expiresAt: now.Add(s.tokenTTL)}
	return raw, s.tokenTTL, nil
}

// authorize consumes a single-use token and returns the room it was minted
// for. The caller's raw token slice is zeroed before returning.
func (s *Store) authorize(token []byte) (string, error) {
	defer clear(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorizeLocked(token)
}

func (s *Store) authorizeLocked(token []byte) (string, error) {
	key := sha256.Sum256(token)
	info, ok := s.tokens[key]
	if !ok {
		return "", ErrInvalidToken
	}
	delete(s.tokens, key)
	if !info.expiresAt.After(s.now()) {
		return "", ErrInvalidToken
	}
	return info.roomID, nil
}

// Begin reserves fileID. The entry enters the map immediately in the
// uploading state so duplicate IDs are rejected and wipes can reclaim a
// half-uploaded file whose connection went silent.
func (s *Store) Begin(fileID, roomID string) (*Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.files[fileID]; exists {
		return nil, ErrDuplicateID
	}
	e := &entry{id: fileID, roomID: roomID, state: stateUploading}
	e.armTimerLocked(s, s.uploadWindow)
	s.files[fileID] = e
	return &Upload{entry: e}, nil
}

// Upload is the handle for one in-progress upload.
type Upload struct {
	entry *entry
}

// ReadChunk reads exactly one frame ([12 IV][4 BE length][ciphertext]) from r
// into a freshly allocated chunk, sized exactly once and never resized, so no
// reallocation copy of ciphertext can linger. It returns io.EOF on a clean
// stream end at a frame boundary.
func (u *Upload) ReadChunk(r io.Reader) error {
	var header [ChunkHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		return err
	}
	length := int(binary.BigEndian.Uint32(header[12:16]))
	if length < MinChunkBody || length > MaxChunkBody+MinChunkBody {
		return ErrBadFrame
	}
	chunk := make([]byte, ChunkHeaderSize+length)
	copy(chunk, header[:])
	if _, err := io.ReadFull(r, chunk[ChunkHeaderSize:]); err != nil {
		clear(chunk) // zero the partial read before discarding the buffer
		return err
	}
	e := u.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.wiped {
		clear(chunk)
		return ErrWiped
	}
	e.chunks = append(e.chunks, chunk)
	e.size += int64(len(chunk))
	return nil
}

// Complete finalizes the upload: the file becomes downloadable and its TTL
// timer replaces the upload-window timer. The Upload is dead after this call.
func (s *Store) Complete(u *Upload) (time.Time, error) {
	e := u.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.wiped {
		return time.Time{}, ErrWiped
	}
	if e.state != stateUploading {
		return time.Time{}, ErrWrongState
	}
	e.state = stateReady
	expiresAt := s.now().Add(s.ttl)
	e.armTimerLocked(s, s.ttl)
	return expiresAt, nil
}

// Abort discards an upload, zeroing every buffered chunk. Idempotent.
func (s *Store) Abort(u *Upload) {
	e := u.entry
	e.mu.Lock()
	e.wipeLocked()
	e.mu.Unlock()
	s.removeEntry(e.id, e)
}

// expire runs on a timer goroutine for one timer generation: the upload
// window, the TTL, or the deferral hard cap. It never holds Store.mu while
// waiting for an entry lock.
func (s *Store) expire(fileID string, gen uint64) {
	s.mu.Lock()
	e, ok := s.files[fileID]
	s.mu.Unlock()
	if !ok {
		return
	}
	e.mu.Lock()
	if e.wiped || e.timerGen != gen {
		e.mu.Unlock()
		return
	}
	switch e.state {
	case stateUploading:
		// Upload window elapsed: reclaim silently, nothing was announced.
		e.wipeLocked()
		e.mu.Unlock()
		s.removeEntry(fileID, e)
	case stateReady:
		roomID := e.roomID
		if e.downloads == 0 {
			e.wipeLocked()
			e.mu.Unlock()
			s.removeEntry(fileID, e)
		} else {
			// Requirement: a download in progress at expiry runs to
			// completion. Deny new downloads, defer the wipe.
			e.state = stateExpiredDeferred
			e.armTimerLocked(s, s.deferralCap)
			e.mu.Unlock()
		}
		if s.cfg.OnExpired != nil {
			s.cfg.OnExpired(roomID, fileID)
		}
	case stateExpiredDeferred:
		// Deferral cap reached with downloads still active: force-abort them.
		e.wipeLocked()
		e.mu.Unlock()
		s.removeEntry(fileID, e)
	}
}

// OpenDownload consumes the token, verifies it was minted for the file's room,
// and registers a streaming download. Cancelling ctx aborts it; a force wipe
// cancels it from the store side.
func (s *Store) OpenDownload(ctx context.Context, token []byte, fileID string) (*Download, error) {
	defer clear(token)
	s.mu.Lock()
	roomID, err := s.authorizeLocked(token)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	e, ok := s.files[fileID]
	s.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	e.mu.Lock()
	switch {
	case e.wiped:
		e.mu.Unlock()
		return nil, ErrNotFound
	case e.state != stateReady:
		e.mu.Unlock()
		return nil, ErrNotReady
	case e.roomID != roomID:
		e.mu.Unlock()
		return nil, ErrNotAuthorized
	}
	dlCtx, cancel := context.WithCancel(ctx)
	e.downloads++
	e.nextDLID++
	dlID := e.nextDLID
	e.dls = append(e.dls, activeDownload{id: dlID, cancel: cancel})
	size := e.size
	e.mu.Unlock()
	return &Download{s: s, entry: e, id: dlID, ctx: dlCtx, cancel: cancel, size: size}, nil
}

// Download streams one file's stored frames.
type Download struct {
	s      *Store
	entry  *entry
	id     uint64
	ctx    context.Context // child of the request ctx; cancelled by Close and by force wipes
	cancel context.CancelFunc
	size   int64
}

// Size returns the total framed byte count (served as Content-Length).
func (d *Download) Size() int64 { return d.size }

// StreamTo streams every stored frame verbatim, in order. Each chunk write
// happens under the entry lock, so a concurrent wipe can never race a
// mid-write chunk. The HTTP layer bounds each write with a deadline so a
// stalled reader cannot pin the lock. The download's context — tied to the
// request and to force wipes — is checked between chunks.
func (d *Download) StreamTo(w io.Writer) error {
	e := d.entry
	for i := 0; ; i++ {
		e.mu.Lock()
		if e.wiped {
			e.mu.Unlock()
			return ErrWiped
		}
		if i >= len(e.chunks) {
			e.mu.Unlock()
			return nil
		}
		_, err := w.Write(e.chunks[i])
		e.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-d.ctx.Done():
			return d.ctx.Err()
		default:
		}
	}
}

// Close unregisters the download. If the file expired while deferred and this
// was the last active download, it is wiped immediately — the file "may run to
// completion, then disappears" rule.
func (d *Download) Close() {
	e := d.entry
	d.cancel()
	e.mu.Lock()
	e.downloads--
	for i, dl := range e.dls {
		if dl.id == d.id {
			e.dls = append(e.dls[:i], e.dls[i+1:]...)
			break
		}
	}
	last := e.downloads == 0 && e.state == stateExpiredDeferred && !e.wiped
	if last {
		e.wipeLocked()
	}
	e.mu.Unlock()
	if last {
		d.s.removeEntry(e.id, e)
	}
}

// WipeRoom force-wipes every file and token belonging to roomID. Called when
// the last member leaves: nobody can download anymore, so nothing may remain.
// OnExpired is not invoked.
func (s *Store) WipeRoom(roomID string) {
	s.mu.Lock()
	stale := make([]*entry, 0, len(s.files))
	for id, e := range s.files {
		if e.roomID == roomID {
			stale = append(stale, e)
			delete(s.files, id)
		}
	}
	for key, info := range s.tokens {
		if info.roomID == roomID {
			delete(s.tokens, key)
		}
	}
	s.mu.Unlock()
	for _, e := range stale {
		e.mu.Lock()
		e.wipeLocked()
		e.mu.Unlock()
	}
}

// RevokeConnection purges a departing connection's live tokens.
func (s *Store) RevokeConnection(connID string) {
	s.mu.Lock()
	for key, info := range s.tokens {
		if info.connID == connID {
			delete(s.tokens, key)
		}
	}
	s.mu.Unlock()
}

// WipeAll force-wipes everything (shutdown path). OnExpired is not invoked.
func (s *Store) WipeAll() {
	s.mu.Lock()
	stale := make([]*entry, 0, len(s.files))
	for id, e := range s.files {
		stale = append(stale, e)
		delete(s.files, id)
	}
	for key := range s.tokens {
		delete(s.tokens, key)
	}
	s.mu.Unlock()
	for _, e := range stale {
		e.mu.Lock()
		e.wipeLocked()
		e.mu.Unlock()
	}
}

// FileCount and TokenCount are test-only introspection.
func (s *Store) FileCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.files)
}
func (s *Store) TokenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens)
}
