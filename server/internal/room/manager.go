package room

import (
	"crypto/rand"
	"errors"
	"math/big"
	"sync"
)

var (
	ErrNotFound = errors.New("room not found")
	ErrFull     = errors.New("room full")
	ErrCapacity = errors.New("room capacity reached")
	ErrIdentity = errors.New("ephemeral identity already present")
	ErrOwner    = errors.New("owner permission required")
	ErrPending  = errors.New("admission request not found")
)

type Member struct {
	ConnectionID string
	EphemeralID  string
	Nickname     string
	Send         chan<- []byte
	Disconnect   func()
}

type PublicMember struct {
	EphemeralID string `json:"ephemeralId"`
	Nickname    string `json:"nickname"`
}

type Admission struct {
	RequestID string
	PublicKey string
	Member    *Member
}

type LeaveResult struct {
	Deleted      bool
	OwnerChanged bool
	Rejected     []*Admission
}

type Room struct {
	ID                string
	OwnerConnectionID string
	members           map[string]*Member
	pending           map[string]*Admission
	approved          map[string]*Admission
}

// Manager owns only live room/member routing state. It deliberately has no
// message/history collection and no persistence adapter.
type Manager struct {
	mu             sync.Mutex
	rooms          map[string]*Room
	maxRooms       int
	maxMembersRoom int
}

func NewManager(maxRooms, maxMembersRoom int) *Manager {
	return &Manager{rooms: make(map[string]*Room), maxRooms: maxRooms, maxMembersRoom: maxMembersRoom}
}

// Enter admits the first caller to an empty room as its owner, or queues the
// caller for the current owner's approval when the room is already occupied.
// The returned Member is the current owner (nil when the caller was admitted).
func (m *Manager) Enter(roomID string, admission *Admission) (bool, *Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		if len(m.rooms) >= m.maxRooms {
			return false, nil, ErrCapacity
		}
		m.rooms[roomID] = &Room{
			ID:                roomID,
			OwnerConnectionID: admission.Member.ConnectionID,
			members:           map[string]*Member{admission.Member.ConnectionID: admission.Member},
			pending:           make(map[string]*Admission),
			approved:          make(map[string]*Admission),
		}
		return true, nil, nil
	}
	if err := m.checkCapacityAndIdentityLocked(r, admission.Member); err != nil {
		return false, nil, err
	}
	r.pending[admission.RequestID] = admission
	return false, r.members[r.OwnerConnectionID], nil
}

func (m *Manager) checkCapacityAndIdentityLocked(r *Room, member *Member) error {
	if len(r.members)+len(r.pending)+len(r.approved) >= m.maxMembersRoom {
		return ErrFull
	}
	for _, existing := range r.members {
		if existing.EphemeralID == member.EphemeralID {
			return ErrIdentity
		}
	}
	for _, admission := range r.pending {
		if admission.Member.EphemeralID == member.EphemeralID {
			return ErrIdentity
		}
	}
	for _, admission := range r.approved {
		if admission.Member.EphemeralID == member.EphemeralID {
			return ErrIdentity
		}
	}
	return nil
}

func (m *Manager) DecideAdmission(roomID, ownerConnectionID, requestID string, approve bool) (*Admission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return nil, ErrNotFound
	}
	if r.OwnerConnectionID != ownerConnectionID {
		return nil, ErrOwner
	}
	admission, ok := r.pending[requestID]
	if !ok {
		return nil, ErrPending
	}
	delete(r.pending, requestID)
	if approve {
		r.approved[admission.Member.ConnectionID] = admission
	}
	return admission, nil
}

func (m *Manager) CompleteAdmission(roomID, connectionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return ErrNotFound
	}
	admission, ok := r.approved[connectionID]
	if !ok {
		return ErrPending
	}
	delete(r.approved, connectionID)
	r.members[connectionID] = admission.Member
	return nil
}

func (m *Manager) CancelAdmission(roomID, connectionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return
	}
	for id, admission := range r.pending {
		if admission.Member.ConnectionID == connectionID {
			delete(r.pending, id)
		}
	}
	delete(r.approved, connectionID)
}

func (m *Manager) Leave(roomID, connectionID string) LeaveResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return LeaveResult{}
	}
	delete(r.members, connectionID)
	if len(r.members) == 0 {
		rejected := collectAdmissions(r)
		delete(m.rooms, roomID)
		return LeaveResult{Deleted: true, Rejected: rejected}
	}
	if r.OwnerConnectionID != connectionID {
		return LeaveResult{}
	}
	r.OwnerConnectionID = randomMemberID(r.members)
	rejected := make([]*Admission, 0, len(r.pending))
	for id, admission := range r.pending {
		rejected = append(rejected, admission)
		delete(r.pending, id)
	}
	return LeaveResult{OwnerChanged: true, Rejected: rejected}
}

func collectAdmissions(r *Room) []*Admission {
	result := make([]*Admission, 0, len(r.pending)+len(r.approved))
	for _, admission := range r.pending {
		result = append(result, admission)
	}
	for _, admission := range r.approved {
		result = append(result, admission)
	}
	return result
}

func randomMemberID(members map[string]*Member) string {
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(ids))))
	if err != nil {
		return ids[0]
	}
	return ids[index.Int64()]
}

func (m *Manager) Rename(roomID, connectionID, nickname string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return ErrNotFound
	}
	member, ok := r.members[connectionID]
	if !ok {
		return ErrIdentity
	}
	member.Nickname = nickname
	return nil
}

func (m *Manager) Broadcast(roomID, excludeConnectionID string, data []byte) {
	m.mu.Lock()
	r, ok := m.rooms[roomID]
	if !ok {
		m.mu.Unlock()
		return
	}
	recipients := make([]*Member, 0, len(r.members))
	for id, member := range r.members {
		if id != excludeConnectionID {
			recipients = append(recipients, member)
		}
	}
	m.mu.Unlock()
	for _, member := range recipients {
		frame := append([]byte(nil), data...)
		select {
		case member.Send <- frame:
		default:
			if member.Disconnect != nil {
				member.Disconnect()
			}
		}
	}
}

func (m *Manager) Members(roomID string) ([]PublicMember, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rooms[roomID]
	if !ok {
		return nil, ""
	}
	result := make([]PublicMember, 0, len(r.members))
	for _, member := range r.members {
		result = append(result, PublicMember{EphemeralID: member.EphemeralID, Nickname: member.Nickname})
	}
	owner := r.members[r.OwnerConnectionID]
	if owner == nil {
		return result, ""
	}
	return result, owner.EphemeralID
}

func (m *Manager) Has(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rooms[id]
	return ok
}

func (m *Manager) Count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.rooms) }
