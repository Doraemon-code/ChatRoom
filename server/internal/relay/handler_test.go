package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"lan-chat/server/internal/protocol"
	"lan-chat/server/internal/room"
)

const (
	testOrigin = "https://chat.test"
	roomID     = "0001"
	idA        = "01890f6e-74e8-4c72-8f9c-61fca85b71e7"
	idB        = "11890f6e-74e8-4c72-8f9c-61fca85b71e7"
)

func testServer(t *testing.T) (*httptest.Server, *room.Manager) {
	t.Helper()
	m := room.NewManager(8, 5)
	h := NewHandler(m, Config{AllowedOrigins: map[string]struct{}{testOrigin: {}}, MaxConnections: 10, HandshakeTimeout: time.Second, ApprovalTimeout: time.Hour, PingInterval: time.Hour})
	s := httptest.NewServer(h)
	t.Cleanup(func() { h.CloseAll(); s.Close() })
	return s, m
}

func dial(t *testing.T, server *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Origin", origin)
	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(protocol.MaxFrameBytes)
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })
	return c
}

func writeJSON(t *testing.T, c *websocket.Conn, value any) {
	t.Helper()
	data, _ := json.Marshal(value)
	if err := c.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func readType(t *testing.T, c *websocket.Conn, want string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h protocol.Header
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatal(err)
	}
	if h.Type != want {
		t.Fatalf("want %s, got %s", want, h.Type)
	}
	return data
}

func encodedBytes(size int) string {
	return base64.RawURLEncoding.EncodeToString(make([]byte, size))
}

func encodedPublicKey() string {
	key := make([]byte, 65)
	key[0] = 4
	return base64.RawURLEncoding.EncodeToString(key)
}

// enterAsOwner joins an empty room, which the first caller enters directly as owner.
func enterAsOwner(t *testing.T, c *websocket.Conn) {
	t.Helper()
	writeJSON(t, c, protocol.JoinRoom{V: 1, Type: "join_room", RoomID: roomID, EphemeralID: idA, Nickname: "房主甲", PublicKey: encodedPublicKey()})
	readType(t, c, "joined")
	var presence protocol.Presence
	if err := json.Unmarshal(readType(t, c, "presence"), &presence); err != nil {
		t.Fatal(err)
	}
	if presence.OwnerEphemeralID != idA || len(presence.Members) != 1 {
		t.Fatalf("first entrant is not the sole owner: %#v", presence)
	}
}

// requestJoin asks to enter an occupied room and returns the owner-side request.
func requestJoin(t *testing.T, owner, joiner *websocket.Conn) protocol.JoinRequest {
	t.Helper()
	writeJSON(t, joiner, protocol.JoinRoom{V: 1, Type: "join_room", RoomID: roomID, EphemeralID: idB, Nickname: "访客乙", PublicKey: encodedPublicKey()})
	readType(t, joiner, "join_pending")
	var request protocol.JoinRequest
	if err := json.Unmarshal(readType(t, owner, "join_request"), &request); err != nil {
		t.Fatal(err)
	}
	if request.Nickname != "访客乙" {
		t.Fatalf("unexpected join request: %#v", request)
	}
	return request
}

func joinWithApproval(t *testing.T, owner, joiner *websocket.Conn) {
	t.Helper()
	request := requestJoin(t, owner, joiner)
	writeJSON(t, owner, protocol.AdmissionDecision{V: 1, Type: "admission_decision", RequestID: request.RequestID, Approved: true, OwnerPublicKey: encodedPublicKey(), IV: encodedBytes(12), WrappedRoomKey: encodedBytes(48)})
	readType(t, joiner, "join_approved")
	writeJSON(t, joiner, protocol.AdmissionComplete{V: 1, Type: "admission_complete"})
	readType(t, joiner, "joined")
	readType(t, joiner, "presence")
}

func TestFirstJoinAdmitsAsOwner(t *testing.T) {
	s, manager := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	if !manager.Has(roomID) {
		t.Fatal("room not created on first enter")
	}
}

func TestLateJoinGetsNoHistoryAndLastLeaveDeletes(t *testing.T) {
	s, manager := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	retained := protocol.Ciphertext{V: 1, Type: "ciphertext", RoomID: roomID, SenderEphemeralID: idA, MessageID: idB, IV: encodedBytes(12), Ciphertext: encodedBytes(16)}
	writeJSON(t, a, retained)

	b := dial(t, s, testOrigin)
	joinWithApproval(t, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := b.Read(ctx); err == nil {
		t.Fatal("late joiner received retained data")
	}

	_ = b.Close(websocket.StatusNormalClosure, "")
	_ = a.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(time.Second)
	for manager.Has(roomID) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if manager.Has(roomID) {
		t.Fatal("empty room retained")
	}
}

func TestOwnerApprovalRequiredAndRejection(t *testing.T) {
	s, _ := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	b := dial(t, s, testOrigin)
	request := requestJoin(t, a, b)
	writeJSON(t, a, protocol.AdmissionDecision{V: 1, Type: "admission_decision", RequestID: request.RequestID, Approved: false})
	var failure protocol.ErrorMessage
	if err := json.Unmarshal(readType(t, b, "error"), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != "JOIN_REJECTED" {
		t.Fatalf("want JOIN_REJECTED, got %s", failure.Code)
	}
}

func TestOwnerDepartureTransfersOwnershipToRemainingMember(t *testing.T) {
	s, _ := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	b := dial(t, s, testOrigin)
	joinWithApproval(t, a, b)

	_ = a.Close(websocket.StatusNormalClosure, "")

	var presence protocol.Presence
	if err := json.Unmarshal(readType(t, b, "presence"), &presence); err != nil {
		t.Fatal(err)
	}
	if presence.OwnerEphemeralID != idB {
		t.Fatalf("owner not transferred to remaining member: %q", presence.OwnerEphemeralID)
	}
}

// A queued join waits on a human owner, so it outlives the handshake deadline
// and is bounded by the approval window instead.
func TestPendingJoinOutlivesHandshakeDeadline(t *testing.T) {
	m := room.NewManager(8, 5)
	h := NewHandler(m, Config{AllowedOrigins: map[string]struct{}{testOrigin: {}}, MaxConnections: 10, HandshakeTimeout: 250 * time.Millisecond, ApprovalTimeout: 10 * time.Second, PingInterval: time.Hour})
	s := httptest.NewServer(h)
	t.Cleanup(func() { h.CloseAll(); s.Close() })

	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	b := dial(t, s, testOrigin)
	request := requestJoin(t, a, b)

	time.Sleep(time.Second)

	writeJSON(t, a, protocol.AdmissionDecision{V: 1, Type: "admission_decision", RequestID: request.RequestID, Approved: true, OwnerPublicKey: encodedPublicKey(), IV: encodedBytes(12), WrappedRoomKey: encodedBytes(48)})
	readType(t, b, "join_approved")
	writeJSON(t, b, protocol.AdmissionComplete{V: 1, Type: "admission_complete"})
	readType(t, b, "joined")
	readType(t, b, "presence")
}

func TestLargeImageCiphertextIsForwarded(t *testing.T) {
	s, _ := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	b := dial(t, s, testOrigin)
	joinWithApproval(t, a, b)

	payload := make([]byte, 600_000) // a ~450 KiB image plus GCM tag
	envelope := protocol.Ciphertext{V: 1, Type: "ciphertext", RoomID: roomID, SenderEphemeralID: idA, MessageID: idB, IV: encodedBytes(12), Ciphertext: base64.RawURLEncoding.EncodeToString(payload)}
	writeJSON(t, a, envelope)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := b.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got protocol.Ciphertext
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Ciphertext != envelope.Ciphertext {
		t.Fatal("large ciphertext was not forwarded intact")
	}
}

func TestOriginRejected(t *testing.T) {
	s, _ := testServer(t)
	header := http.Header{}
	header.Set("Origin", "https://evil.test")
	_, response, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), &websocket.DialOptions{HTTPHeader: header})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("origin was not rejected: response=%v err=%v", response, err)
	}
}

func TestRenameBroadcastsCurrentPresence(t *testing.T) {
	s, _ := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	writeJSON(t, a, protocol.Rename{V: 1, Type: "rename", Nickname: "福尔摩斯"})
	var presence protocol.Presence
	if err := json.Unmarshal(readType(t, a, "presence"), &presence); err != nil {
		t.Fatal(err)
	}
	if len(presence.Members) != 1 || presence.Members[0].Nickname != "福尔摩斯" {
		t.Fatalf("unexpected presence: %#v", presence.Members)
	}
}

func TestRateLimitClosesConnection(t *testing.T) {
	s, _ := testServer(t)
	a := dial(t, s, testOrigin)
	enterAsOwner(t, a)
	msg := protocol.Ciphertext{V: 1, Type: "ciphertext", RoomID: roomID, SenderEphemeralID: idA, MessageID: idB, IV: encodedBytes(12), Ciphertext: encodedBytes(16)}
	for i := 0; i < 20; i++ {
		data, _ := json.Marshal(msg)
		_ = a.Write(context.Background(), websocket.MessageText, data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		_, data, err := a.Read(ctx)
		if err != nil {
			return
		}
		var response protocol.ErrorMessage
		_ = json.Unmarshal(data, &response)
		if response.Code == "RATE_LIMITED" {
			return
		}
	}
}
