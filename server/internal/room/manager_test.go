package room

import (
	"errors"
	"testing"
)

const roomID = "0001"

func member(id string) *Member {
	return &Member{ConnectionID: id, EphemeralID: "00000000-0000-4000-8000-" + id, Nickname: "成员" + id, Send: make(chan []byte, 1)}
}

func admission(id, requestID string) *Admission {
	return &Admission{RequestID: requestID, PublicKey: "pk", Member: member(id)}
}

func TestFirstEnterCreatesRoomWithOwner(t *testing.T) {
	m := NewManager(8, 5)
	admitted, owner, err := m.Enter(roomID, admission("000000000001", "req-1"))
	if err != nil || !admitted || owner != nil {
		t.Fatalf("first enter should admit as owner: admitted=%v owner=%v err=%v", admitted, owner, err)
	}
	if !m.Has(roomID) {
		t.Fatal("room not created")
	}
	members, currentOwner := m.Members(roomID)
	if len(members) != 1 || currentOwner != members[0].EphemeralID {
		t.Fatalf("creator is not owner: %#v owner=%q", members, currentOwner)
	}
	if result := m.Leave(roomID, "000000000001"); !result.Deleted {
		t.Fatal("last leave must delete the room")
	}
}

func TestSecondEnterQueuesForOwnerApproval(t *testing.T) {
	m := NewManager(8, 5)
	if _, _, err := m.Enter(roomID, admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	first := m.rooms[roomID].members["000000000001"]
	admitted, owner, err := m.Enter(roomID, admission("000000000002", "req-2"))
	if err != nil || admitted {
		t.Fatalf("second enter must wait for approval: admitted=%v err=%v", admitted, err)
	}
	if owner != first {
		t.Fatal("pending join was not routed to the owner")
	}
	if members, _ := m.Members(roomID); len(members) != 1 {
		t.Fatalf("pending joiner counted as member: %#v", members)
	}
	if _, err := m.DecideAdmission(roomID, "000000000002", "req-2", true); !errors.Is(err, ErrOwner) {
		t.Fatalf("non-owner decided admission: %v", err)
	}
	if _, err := m.DecideAdmission(roomID, "000000000001", "missing", true); !errors.Is(err, ErrPending) {
		t.Fatalf("unknown request accepted: %v", err)
	}
	if _, err := m.DecideAdmission(roomID, "000000000001", "req-2", true); err != nil {
		t.Fatal(err)
	}
	if members, _ := m.Members(roomID); len(members) != 1 {
		t.Fatalf("approved joiner joined before admission_complete: %#v", members)
	}
	if err := m.CompleteAdmission(roomID, "000000000002"); err != nil {
		t.Fatal(err)
	}
	if members, _ := m.Members(roomID); len(members) != 2 {
		t.Fatalf("completed joiner missing from presence: %#v", members)
	}
}

func TestRejectedAndCancelledAdmissionsAreDropped(t *testing.T) {
	m := NewManager(8, 5)
	if _, _, err := m.Enter(roomID, admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Enter(roomID, admission("000000000002", "req-reject")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DecideAdmission(roomID, "000000000001", "req-reject", false); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAdmission(roomID, "000000000002"); !errors.Is(err, ErrPending) {
		t.Fatalf("rejected admission completed: %v", err)
	}
	if _, _, err := m.Enter(roomID, admission("000000000003", "req-cancel")); err != nil {
		t.Fatal(err)
	}
	m.CancelAdmission(roomID, "000000000003")
	if _, err := m.DecideAdmission(roomID, "000000000001", "req-cancel", true); !errors.Is(err, ErrPending) {
		t.Fatalf("cancelled admission decided: %v", err)
	}
}

func addMember(t *testing.T, m *Manager, joiningID, requestID string) {
	t.Helper()
	admitted, _, err := m.Enter(roomID, admission(joiningID, requestID))
	if err != nil || admitted {
		t.Fatalf("expected pending admission: admitted=%v err=%v", admitted, err)
	}
	if _, err := m.DecideAdmission(roomID, "000000000001", requestID, true); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAdmission(roomID, joiningID); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerLeaveTransfersAndRejectsPending(t *testing.T) {
	m := NewManager(8, 5)
	if _, _, err := m.Enter(roomID, admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	addMember(t, m, "000000000002", "req-2")
	addMember(t, m, "000000000003", "req-3")
	if _, _, err := m.Enter(roomID, admission("000000000004", "req-pending")); err != nil {
		t.Fatal(err)
	}

	result := m.Leave(roomID, "000000000001")
	if result.Deleted || !result.OwnerChanged {
		t.Fatalf("unexpected leave result: %#v", result)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].RequestID != "req-pending" {
		t.Fatalf("pending admission not rejected on owner change: %#v", result.Rejected)
	}
	members, newOwner := m.Members(roomID)
	if len(members) != 2 {
		t.Fatalf("unexpected members after transfer: %#v", members)
	}
	if newOwner == "" || newOwner == member("000000000001").EphemeralID {
		t.Fatalf("owner not transferred to a remaining member: %q", newOwner)
	}
}

func TestNonOwnerLeaveKeepsOwner(t *testing.T) {
	m := NewManager(8, 5)
	if _, _, err := m.Enter(roomID, admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	addMember(t, m, "000000000002", "req-2")
	result := m.Leave(roomID, "000000000002")
	if result.OwnerChanged || result.Deleted {
		t.Fatalf("non-owner departure changed room state: %#v", result)
	}
	if _, currentOwner := m.Members(roomID); currentOwner != member("000000000001").EphemeralID {
		t.Fatalf("owner changed on ordinary leave: %q", currentOwner)
	}
}

func TestLastLeaveDeletesAndRejectsPending(t *testing.T) {
	m := NewManager(8, 5)
	if _, _, err := m.Enter(roomID, admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Enter(roomID, admission("000000000002", "req-pending")); err != nil {
		t.Fatal(err)
	}
	result := m.Leave(roomID, "000000000001")
	if !result.Deleted || len(result.Rejected) != 1 {
		t.Fatalf("last leave must delete and reject pending: %#v", result)
	}
	if m.Has(roomID) {
		t.Fatal("empty room retained")
	}
}

func TestCapacityLimits(t *testing.T) {
	full := NewManager(8, 1)
	if _, _, err := full.Enter(roomID, admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := full.Enter(roomID, admission("000000000002", "req-2")); !errors.Is(err, ErrFull) {
		t.Fatalf("want full, got %v", err)
	}
	small := NewManager(1, 5)
	if _, _, err := small.Enter("0001", admission("000000000001", "req-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := small.Enter("0002", admission("000000000002", "req-2")); !errors.Is(err, ErrCapacity) {
		t.Fatalf("want capacity, got %v", err)
	}
}

func TestDuplicateIdentityIsRejected(t *testing.T) {
	m := NewManager(8, 5)
	first := admission("000000000001", "req-1")
	if _, _, err := m.Enter(roomID, first); err != nil {
		t.Fatal(err)
	}
	duplicate := &Admission{RequestID: "req-2", PublicKey: "pk", Member: &Member{ConnectionID: "000000000099", EphemeralID: first.Member.EphemeralID, Nickname: "冒名者", Send: make(chan []byte, 1)}}
	if _, _, err := m.Enter(roomID, duplicate); !errors.Is(err, ErrIdentity) {
		t.Fatalf("want identity error, got %v", err)
	}
}

func TestUnknownRoomLookups(t *testing.T) {
	m := NewManager(8, 5)
	if _, err := m.DecideAdmission(roomID, "c", "req", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := m.CompleteAdmission(roomID, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if err := m.Rename(roomID, "c", "福尔摩斯"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestFreshManagerHasNoRecoveredRooms(t *testing.T) {
	old := NewManager(8, 5)
	_, _, _ = old.Enter(roomID, admission("000000000001", "req-1"))
	fresh := NewManager(8, 5)
	if fresh.Count() != 0 {
		t.Fatal("fresh process state recovered a room")
	}
}

func TestRenameIsRuntimeOnly(t *testing.T) {
	m := NewManager(8, 5)
	_, _, _ = m.Enter(roomID, admission("000000000001", "req-1"))
	if err := m.Rename(roomID, "000000000001", "福尔摩斯"); err != nil {
		t.Fatal(err)
	}
	members, _ := m.Members(roomID)
	if len(members) != 1 || members[0].Nickname != "福尔摩斯" {
		t.Fatalf("rename not reflected in live presence: %#v", members)
	}
	m.Leave(roomID, "000000000001")
	if m.Has(roomID) {
		t.Fatal("renamed identity retained after empty-room deletion")
	}
}
