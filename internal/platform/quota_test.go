package platform

import (
	"context"
	"sync"
	"testing"

	"github.com/yuebanhome/openmajiang/internal/auth"
	"github.com/yuebanhome/openmajiang/rules/mcr"
)

func quotaUser(t *testing.T, s *Service) auth.User {
	t.Helper()
	u := auth.User{ID: id("user"), Name: "Quota", Verified: true, Status: "active"}
	if _, e := s.pool.Exec(context.Background(), `INSERT INTO auth_users(id,email,name,password_hash,verified) VALUES($1,$2,$3,'test',true)`, u.ID, u.ID+"@example.invalid", u.Name); e != nil {
		t.Fatal(e)
	}
	return u
}
func TestPGRoomQuotaIsAtomicAcrossConcurrentCreates(t *testing.T) {
	s := recoveryService(t, mcr.New())
	s.cfg.MaxOwnerWaitingRooms = 4
	owner := quotaUser(t, s)
	var wg sync.WaitGroup
	codes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := s.create(context.Background(), owner, createRequest{Mode: "bot_only", Format: "practice_1"})
			codes <- recoveryCode(e)
		}()
	}
	wg.Wait()
	close(codes)
	ok, limited := 0, 0
	for c := range codes {
		switch c {
		case "":
			ok++
		case "WAITING_ROOM_LIMIT":
			limited++
		default:
			t.Fatalf("unexpected quota error %s", c)
		}
	}
	if ok != 4 || limited != 4 {
		t.Fatalf("quota oversubscribed successes=%d limited=%d", ok, limited)
	}
}
func TestPGGlobalWaitingAndQueueCapsDoNotOversubscribe(t *testing.T) {
	s := recoveryService(t, mcr.New())
	s.cfg.MaxWaitingRooms = 2
	s.cfg.MaxQueuedParticipants = 1
	users := []auth.User{quotaUser(t, s), quotaUser(t, s), quotaUser(t, s)}
	var wg sync.WaitGroup
	codes := make(chan string, 3)
	for _, u := range users {
		wg.Add(1)
		go func(u auth.User) {
			defer wg.Done()
			_, _, e := s.create(context.Background(), u, createRequest{Mode: "bot_only", Format: "practice_1"})
			codes <- recoveryCode(e)
		}(u)
	}
	wg.Wait()
	close(codes)
	success := 0
	for c := range codes {
		if c == "" {
			success++
		} else if c != "ROOM_CAPACITY_REACHED" {
			t.Fatalf("unexpected create result %s", c)
		}
	}
	if success != 2 {
		t.Fatalf("global room cap allowed %d", success)
	}
	codes = make(chan string, 3)
	for _, u := range users {
		wg.Add(1)
		go func(u auth.User) {
			defer wg.Done()
			codes <- recoveryCode(s.enqueue(context.Background(), u.ID, "", queueRequest{Format: "practice_1"}))
		}(u)
	}
	wg.Wait()
	close(codes)
	success = 0
	for c := range codes {
		if c == "" {
			success++
		} else if c != "QUEUE_CAPACITY_REACHED" {
			t.Fatalf("unexpected queue result %s", c)
		}
	}
	if success != 1 {
		t.Fatalf("global queue cap allowed %d", success)
	}
}
