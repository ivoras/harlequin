package docevents

import "testing"

func recv(s *Sub) (Event, bool) {
	select {
	case ev := <-s.C:
		return ev, true
	default:
		return Event{}, false
	}
}

func TestVisibility(t *testing.T) {
	b := New()
	alice := b.Subscribe(1, func(p int64) bool { return p == 7 })
	bob := b.Subscribe(2, nil)

	b.Publish(Event{Op: Created, Scope: "personal", UserID: 1, DocID: 5})
	if _, ok := recv(alice); !ok {
		t.Error("owner should see their personal doc")
	}
	if _, ok := recv(bob); ok {
		t.Error("other users must not see a personal doc")
	}

	b.Publish(Event{Op: Deleted, Scope: "shared", DocID: 6})
	if _, ok := recv(alice); !ok {
		t.Error("shared events go to everyone")
	}
	if _, ok := recv(bob); !ok {
		t.Error("shared events go to everyone")
	}

	b.Publish(Event{Op: Updated, Scope: "project", ProjectID: 7, DocID: 1})
	if _, ok := recv(alice); !ok {
		t.Error("project member should see project doc event")
	}
	if _, ok := recv(bob); ok {
		t.Error("non-member must not see project doc event")
	}
	b.Publish(Event{Op: Updated, Scope: "project", ProjectID: 8, DocID: 1})
	if _, ok := recv(alice); ok {
		t.Error("member of project 7 must not see project 8")
	}
}

func TestNilBusAndUnsubscribe(t *testing.T) {
	var nb *Bus
	nb.Publish(Event{Scope: "shared"}) // must not panic
	nb.Unsubscribe(nb.Subscribe(1, nil))

	b := New()
	s := b.Subscribe(1, nil)
	b.Unsubscribe(s)
	b.Publish(Event{Scope: "shared"})
	if _, ok := recv(s); ok {
		t.Error("unsubscribed sub should not receive")
	}
}

func TestPublishNeverBlocks(t *testing.T) {
	b := New()
	b.Subscribe(1, nil)
	for i := 0; i < 1000; i++ {
		b.Publish(Event{Scope: "shared"})
	}
}
