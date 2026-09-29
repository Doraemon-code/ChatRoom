package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Version = 1
	// MaxFrameBytes bounds one WebSocket frame. A compressed image message —
	// base64 ciphertext of up to MaxPlaintext bytes plus envelope overhead —
	// must fit inside it.
	MaxFrameBytes = 2 << 20
	// MaxPlaintext bounds the encrypted payload (text or image) in bytes.
	MaxPlaintext = 1 << 20
	// maxCiphertextB64 is the largest base64url ciphertext length the decoder
	// accepts: MaxPlaintext bytes of payload plus the 16-byte GCM tag.
	maxCiphertextB64 = 1_400_000
)

var (
	// The room number is one of the eight fixed rooms.
	roomPattern    = regexp.MustCompile(`^000[1-8]$`)
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	requestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`)
	b64urlPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type Header struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}

// JoinRoom asks to enter a fixed room. The first caller to reach an empty room
// becomes its owner and is admitted immediately; later callers are queued for
// the current owner's approval.
type JoinRoom struct {
	V           int    `json:"v"`
	Type        string `json:"type"`
	RoomID      string `json:"roomId"`
	EphemeralID string `json:"ephemeralId"`
	Nickname    string `json:"nickname"`
	PublicKey   string `json:"publicKey"`
}
type Rename struct {
	V        int    `json:"v"`
	Type     string `json:"type"`
	Nickname string `json:"nickname"`
}
type AdmissionDecision struct {
	V              int    `json:"v"`
	Type           string `json:"type"`
	RequestID      string `json:"requestId"`
	Approved       bool   `json:"approved"`
	OwnerPublicKey string `json:"ownerPublicKey,omitempty"`
	IV             string `json:"iv,omitempty"`
	WrappedRoomKey string `json:"wrappedRoomKey,omitempty"`
}
type AdmissionComplete struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}
type Ciphertext struct {
	V                 int    `json:"v"`
	Type              string `json:"type"`
	RoomID            string `json:"roomId"`
	SenderEphemeralID string `json:"senderEphemeralId"`
	MessageID         string `json:"messageId"`
	IV                string `json:"iv"`
	Ciphertext        string `json:"ciphertext"`
}

// FileTokenRequest asks the relay for a single-use HTTP transfer token bound
// to the sender's current room. Only legal from a joined connection; the token
// authorizes exactly one /api/file upload or download.
type FileTokenRequest struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}

type ErrorMessage struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Code string `json:"code"`
}
type Joined struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}
type JoinRequest struct {
	V           int    `json:"v"`
	Type        string `json:"type"`
	RequestID   string `json:"requestId"`
	EphemeralID string `json:"ephemeralId"`
	Nickname    string `json:"nickname"`
	PublicKey   string `json:"publicKey"`
}
type JoinPending struct {
	V    int    `json:"v"`
	Type string `json:"type"`
}
type JoinApproved struct {
	V              int    `json:"v"`
	Type           string `json:"type"`
	RequestID      string `json:"requestId"`
	OwnerPublicKey string `json:"ownerPublicKey"`
	IV             string `json:"iv"`
	WrappedRoomKey string `json:"wrappedRoomKey"`
}
type Member struct {
	EphemeralID string `json:"ephemeralId"`
	Nickname    string `json:"nickname"`
}
type Presence struct {
	V                int      `json:"v"`
	Type             string   `json:"type"`
	OwnerEphemeralID string   `json:"ownerEphemeralId"`
	Members          []Member `json:"members"`
}

// FileToken carries a one-time bearer capability for a single HTTP file
// operation. The relay stores only SHA-256(token); the raw bytes exist solely
// in this frame and the client's memory.
type FileToken struct {
	V         int    `json:"v"`
	Type      string `json:"type"`
	Token     string `json:"token"`
	ExpiresIn int    `json:"expiresIn"`
}

// FileExpired is broadcast to the room when a stored file reaches its TTL,
// whether the wipe happens immediately or after the last active download
// finishes. Clients flip the file card to its expired state.
type FileExpired struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	FileID string `json:"fileId"`
}

func Decode(data []byte) (any, error) {
	if len(data) == 0 || len(data) > MaxFrameBytes {
		return nil, errors.New("invalid frame size")
	}
	var h Header
	if err := json.Unmarshal(data, &h); err != nil || h.V != Version {
		return nil, errors.New("invalid header")
	}
	var target any
	switch h.Type {
	case "join_room":
		target = &JoinRoom{}
	case "rename":
		target = &Rename{}
	case "admission_decision":
		target = &AdmissionDecision{}
	case "admission_complete":
		target = &AdmissionComplete{}
	case "ciphertext":
		target = &Ciphertext{}
	case "file_token_request":
		target = &FileTokenRequest{}
	default:
		return nil, errors.New("unknown type")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return nil, errors.New("invalid message")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data")
	}
	if err := validate(target); err != nil {
		return nil, err
	}
	return target, nil
}

func validIdentity(roomID, ephemeralID, nickname string) bool {
	return roomPattern.MatchString(roomID) && uuidPattern.MatchString(ephemeralID) && validNickname(nickname)
}

// ValidUUID reports whether value is a canonical lowercase UUIDv4. File IDs
// minted by clients and echoed in file_expired frames must match it.
func ValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

func validNickname(value string) bool {
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
	return count >= 2 && count <= 24
}

func validate(v any) error {
	switch m := v.(type) {
	case *JoinRoom:
		if !validIdentity(m.RoomID, m.EphemeralID, m.Nickname) || !validPublicKey(m.PublicKey) {
			return errors.New("invalid join")
		}
	case *Rename:
		if !validNickname(m.Nickname) {
			return errors.New("invalid nickname")
		}
	case *AdmissionDecision:
		if !requestPattern.MatchString(m.RequestID) {
			return errors.New("invalid admission request")
		}
		if m.Approved {
			if !validPublicKey(m.OwnerPublicKey) || !validEncoded(m.IV, 12, 12) || !validEncoded(m.WrappedRoomKey, 48, 48) {
				return errors.New("invalid admission key envelope")
			}
		} else if m.OwnerPublicKey != "" || m.IV != "" || m.WrappedRoomKey != "" {
			return errors.New("unexpected rejected admission fields")
		}
	case *AdmissionComplete:
		// Strict decoder already guarantees this message has no extra payload.
	case *FileTokenRequest:
		// Strict decoder already guarantees this message has no extra payload.
	case *FileToken:
		if _, ok := decodeFixed(m.Token, 32); !ok {
			return errors.New("invalid file token")
		}
		if m.ExpiresIn < 1 || m.ExpiresIn > 600 {
			return errors.New("invalid file token expiry")
		}
	case *FileExpired:
		if !ValidUUID(m.FileID) {
			return errors.New("invalid file id")
		}
	case *Ciphertext:
		if !roomPattern.MatchString(m.RoomID) || !uuidPattern.MatchString(m.SenderEphemeralID) || !uuidPattern.MatchString(m.MessageID) {
			return errors.New("invalid ciphertext identity")
		}
		iv, ok := decodeFixed(m.IV, 12)
		if !ok {
			return errors.New("invalid iv")
		}
		_ = iv
		if !b64urlPattern.MatchString(m.Ciphertext) || len(m.Ciphertext) < 22 || len(m.Ciphertext) > maxCiphertextB64 {
			return errors.New("invalid ciphertext")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(m.Ciphertext)
		if err != nil || len(decoded) < 16 || len(decoded) > MaxPlaintext+16 {
			return errors.New("invalid ciphertext size")
		}
	default:
		return errors.New("unsupported message")
	}
	return nil
}

func decodeFixed(s string, n int) ([]byte, bool) {
	if !b64urlPattern.MatchString(s) {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	return b, err == nil && len(b) == n
}

func validEncoded(value string, min, max int) bool {
	if !b64urlPattern.MatchString(value) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) >= min && len(decoded) <= max
}

func validPublicKey(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 65 && decoded[0] == 4
}

func Marshal(v any) []byte { b, _ := json.Marshal(v); return b }
