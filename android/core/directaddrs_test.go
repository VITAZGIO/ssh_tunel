package core

import (
	"net"
	"testing"
	"time"
)

func TestDirectAddrsRemembersAndExpires(t *testing.T) {
	m := NewDirectAddrs()
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }

	m.Add("ads.example", []net.IP{net.ParseIP("203.0.113.7")})
	if name, ok := m.Name("203.0.113.7"); !ok || name != "ads.example" {
		t.Fatalf("Name = %q, %v", name, ok)
	}
	if _, ok := m.Name("203.0.113.8"); ok {
		t.Fatal("чужой адрес не должен находиться")
	}
	now = now.Add(directAddrsTTL + time.Second)
	if _, ok := m.Name("203.0.113.7"); ok {
		t.Fatal("устаревшая запись должна забываться")
	}
}

func TestDNSRemembersDirectAnswers(t *testing.T) {
	pool, err := NewFakePool("198.18.128.0/17")
	if err != nil {
		t.Fatal(err)
	}
	mem := NewDirectAddrs()
	d := &DNS{
		Pool:     pool,
		Remember: mem,
		Direct:   func(name string) bool { return name == "direct.example" },
		Local: func(string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		},
	}
	if _, err := d.resolve("direct.example"); err != nil {
		t.Fatal(err)
	}
	fake, _ := d.resolve("tunnel.example")

	resolve := ChainResolvers(pool.Resolver(), mem.Name)
	if name, ok := resolve("203.0.113.9"); !ok || name != "direct.example" {
		t.Fatalf("настоящий адрес прямого имени: %q, %v", name, ok)
	}
	if name, ok := resolve(fake[0].String()); !ok || name != "tunnel.example" {
		t.Fatalf("подставной адрес: %q, %v", name, ok)
	}
}
