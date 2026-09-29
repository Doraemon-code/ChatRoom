package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"lan-chat/server/internal/filetransfer"
	"lan-chat/server/internal/protocol"
	"lan-chat/server/internal/room"
)

const outboundQueue = 32

type Config struct {
	AllowedOrigins   map[string]struct{}
	MaxConnections   int
	HandshakeTimeout time.Duration
	// ApprovalTimeout bounds how long a queued join may wait for the human owner
	// to decide. It starts when the room replies join_pending.
	ApprovalTimeout time.Duration
	PingInterval    time.Duration
	// Files, when set, backs the file_token_request flow: the relay mints
	// single-use HTTP transfer tokens bound to the sender's room, and wipes
	// stored files when the last member leaves. Nil disables file transfer.
	Files *filetransfer.Store
}

type Handler struct {
	rooms       *room.Manager
	config      Config
	slots       chan struct{}
	mu          sync.Mutex
	connections map[string]context.CancelFunc
}

func NewHandler(rooms *room.Manager, config Config) *Handler {
	return &Handler{rooms: rooms, config: config, slots: make(chan struct{}, config.MaxConnections), connections: make(map[string]context.CancelFunc)}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fixedHTTPError(w, http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.config.AllowedOrigins[r.Header.Get("Origin")]; !ok {
		fixedHTTPError(w, http.StatusForbidden)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		fixedHTTPError(w, http.StatusServiceUnavailable)
		return
	}

	// Origin was matched against complete configured origins above. Disable the
	// library's host-only default check so reverse-proxy test/deployment hosts do
	// not override that stricter application allowlist.
	connectionID, err := randomID(18)
	if err != nil {
		fixedHTTPError(w, http.StatusServiceUnavailable)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c.SetReadLimit(protocol.MaxFrameBytes)
	ctx, cancel := context.WithCancel(r.Context())
	h.register(connectionID, cancel)
	defer h.unregister(connectionID)
	defer cancel()
	defer c.Close(websocket.StatusNormalClosure, "")
	h.serveConnection(ctx, c, connectionID, cancel)
}

func fixedHTTPError(w http.ResponseWriter, status int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}

func randomID(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (h *Handler) register(id string, cancel context.CancelFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connections[id] = cancel
}
func (h *Handler) unregister(id string) { h.mu.Lock(); defer h.mu.Unlock(); delete(h.connections, id) }
func (h *Handler) CloseAll() {
	h.mu.Lock()
	cs := make([]context.CancelFunc, 0, len(h.connections))
	for _, c := range h.connections {
		cs = append(cs, c)
	}
	h.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

type connState int

const (
	awaitingAction connState = iota
	awaitingApproval
	joined
)

type pendingJoin struct {
	roomID, ephemeralID, nickname, publicKey, requestID string
}

func (h *Handler) serveConnection(ctx context.Context, c *websocket.Conn, connectionID string, cancel context.CancelFunc) {
	send := make(chan []byte, outboundQueue)
	writerDone := make(chan struct{})
	go h.writer(ctx, c, send, writerDone)

	state := awaitingAction
	var currentRoom, currentEphemeral string
	var pending pendingJoin
	member := &room.Member{ConnectionID: connectionID, Send: send, Disconnect: cancel}
	handshakeDeadline := time.Now().Add(h.config.HandshakeTimeout)
	var approvalDeadline time.Time
	bucket := newBucket(12, 5)

	defer func() {
		if state == joined {
			result := h.rooms.Leave(currentRoom, connectionID)
			for _, rejected := range result.Rejected {
				h.enqueue(rejected.Member.Send, errorFrame("OWNER_CHANGED"))
			}
			if !result.Deleted {
				h.broadcastPresence(currentRoom)
			} else if h.config.Files != nil {
				// The room is gone: nobody can download anymore, so its
				// stored ciphertext must not outlive it.
				h.config.Files.WipeRoom(currentRoom)
			}
		} else if pending.roomID != "" {
			h.rooms.CancelAdmission(pending.roomID, connectionID)
		}
		if h.config.Files != nil {
			h.config.Files.RevokeConnection(connectionID)
		}
		cancel()
		select {
		case <-writerDone:
		case <-time.After(time.Second):
		}
	}()

	for {
		readCtx := ctx
		var readCancel context.CancelFunc = func() {}
		switch state {
		case awaitingAction:
			readCtx, readCancel = context.WithDeadline(ctx, handshakeDeadline)
		case awaitingApproval:
			readCtx, readCancel = context.WithDeadline(ctx, approvalDeadline)
		}
		messageType, data, err := c.Read(readCtx)
		readCancel()
		if err != nil {
			return
		}
		if messageType != websocket.MessageText || !bucket.Allow() {
			h.enqueue(send, errorFrame("RATE_LIMITED"))
			return
		}
		decoded, err := protocol.Decode(data)
		if err != nil {
			h.enqueue(send, errorFrame("INVALID_MESSAGE"))
			return
		}

		switch msg := decoded.(type) {
		case *protocol.JoinRoom:
			if state != awaitingAction {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			member.EphemeralID, member.Nickname = msg.EphemeralID, msg.Nickname
			requestID, randomErr := randomID(18)
			if randomErr != nil {
				return
			}
			admission := &room.Admission{RequestID: requestID, PublicKey: msg.PublicKey, Member: member}
			admitted, owner, enterErr := h.rooms.Enter(msg.RoomID, admission)
			if enterErr != nil {
				h.enqueue(send, errorFrame(mapRoomError(enterErr)))
				continue
			}
			if admitted {
				state, currentRoom, currentEphemeral = joined, msg.RoomID, msg.EphemeralID
				h.enqueue(send, protocol.Marshal(protocol.Joined{V: 1, Type: "joined"}))
				h.broadcastPresence(currentRoom)
				continue
			}
			if owner == nil {
				h.enqueue(send, errorFrame("ROOM_NOT_FOUND"))
				return
			}
			pending = pendingJoin{roomID: msg.RoomID, ephemeralID: msg.EphemeralID, nickname: msg.Nickname, publicKey: msg.PublicKey, requestID: requestID}
			state, currentRoom, currentEphemeral = awaitingApproval, msg.RoomID, msg.EphemeralID
			approvalDeadline = time.Now().Add(h.config.ApprovalTimeout)
			h.enqueue(send, protocol.Marshal(protocol.JoinPending{V: 1, Type: "join_pending"}))
			h.enqueue(owner.Send, protocol.Marshal(protocol.JoinRequest{V: 1, Type: "join_request", RequestID: requestID, EphemeralID: msg.EphemeralID, Nickname: msg.Nickname, PublicKey: msg.PublicKey}))

		case *protocol.AdmissionDecision:
			if state != joined {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			admission, err := h.rooms.DecideAdmission(currentRoom, connectionID, msg.RequestID, msg.Approved)
			if err != nil {
				h.enqueue(send, errorFrame(mapRoomError(err)))
				continue
			}
			if !msg.Approved {
				h.enqueue(admission.Member.Send, errorFrame("JOIN_REJECTED"))
				continue
			}
			h.enqueue(admission.Member.Send, protocol.Marshal(protocol.JoinApproved{V: 1, Type: "join_approved", RequestID: msg.RequestID, OwnerPublicKey: msg.OwnerPublicKey, IV: msg.IV, WrappedRoomKey: msg.WrappedRoomKey}))

		case *protocol.AdmissionComplete:
			if state != awaitingApproval {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			if err := h.rooms.CompleteAdmission(currentRoom, connectionID); err != nil {
				h.enqueue(send, errorFrame(mapRoomError(err)))
				return
			}
			state = joined
			h.enqueue(send, protocol.Marshal(protocol.Joined{V: 1, Type: "joined"}))
			h.broadcastPresence(currentRoom)

		case *protocol.Ciphertext:
			if state != joined || msg.RoomID != currentRoom || msg.SenderEphemeralID != currentEphemeral {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			h.rooms.Broadcast(currentRoom, connectionID, data)

		case *protocol.Rename:
			if state != joined {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			if err := h.rooms.Rename(currentRoom, connectionID, msg.Nickname); err != nil {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			h.broadcastPresence(currentRoom)

		case *protocol.FileTokenRequest:
			if state != joined {
				h.enqueue(send, errorFrame("INVALID_MESSAGE"))
				return
			}
			if h.config.Files == nil {
				h.enqueue(send, errorFrame("FILE_DISABLED"))
				continue
			}
			token, ttl, err := h.config.Files.MintToken(connectionID, currentRoom)
			if err != nil {
				h.enqueue(send, errorFrame("FILE_TOKEN_LIMIT"))
				continue
			}
			granted := protocol.Marshal(protocol.FileToken{
				V:         protocol.Version,
				Type:      "file_token",
				Token:     base64.RawURLEncoding.EncodeToString(token),
				ExpiresIn: max(int(ttl.Round(time.Second).Seconds()), 1),
			})
			clear(token)
			h.enqueue(send, granted)
		}
	}
}

func (h *Handler) writer(ctx context.Context, c *websocket.Conn, send <-chan []byte, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.drain(c, send)
			return
		case data := <-send:
			// Deliberately not derived from ctx: teardown cancels ctx, and a
			// queued frame selected in the same instant must still go out —
			// the deferred c.Close bounds this write from the other side.
			writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := c.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// drain flushes frames that were already queued when the connection began
// shutting down — most importantly a just-sent error frame, so the peer learns
// why it was closed. Bounded to a fraction of the close wait: a stalled peer
// cannot delay connection teardown.
func (h *Handler) drain(c *websocket.Conn, send <-chan []byte) {
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case data := <-send:
			writeCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			err := c.Write(writeCtx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		default:
			return
		}
	}
}

func (h *Handler) enqueue(send chan<- []byte, data []byte) {
	select {
	case send <- data:
	default:
	}
}
func errorFrame(code string) []byte {
	return protocol.Marshal(protocol.ErrorMessage{V: 1, Type: "error", Code: code})
}

func (h *Handler) broadcastPresence(roomID string) {
	public, ownerEphemeralID := h.rooms.Members(roomID)
	members := make([]protocol.Member, 0, len(public))
	for _, p := range public {
		members = append(members, protocol.Member{EphemeralID: p.EphemeralID, Nickname: p.Nickname})
	}
	h.rooms.Broadcast(roomID, "", protocol.Marshal(protocol.Presence{V: 1, Type: "presence", OwnerEphemeralID: ownerEphemeralID, Members: members}))
}

func mapRoomError(err error) string {
	switch {
	case errors.Is(err, room.ErrNotFound):
		return "ROOM_NOT_FOUND"
	case errors.Is(err, room.ErrFull):
		return "ROOM_FULL"
	case errors.Is(err, room.ErrCapacity):
		return "CAPACITY_REACHED"
	default:
		return "INVALID_MESSAGE"
	}
}

type bucket struct {
	mu                     sync.Mutex
	tokens, capacity, rate float64
	last                   time.Time
}

func newBucket(capacity, rate float64) *bucket {
	return &bucket{tokens: capacity, capacity: capacity, rate: rate, last: time.Now()}
}
func (b *bucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
