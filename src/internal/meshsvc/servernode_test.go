package meshsvc

// Серверы как узлы сети устройств: у главного и у побочного свои адреса и
// имена .mesh, соединение на них ведёт на 127.0.0.1:ПОРТ самого сервера.

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/mesh"
)

// echoOn — «служба на сервере»: отвечает строкой с меткой.
func echoOn(t *testing.T, tag string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, tag+": "+line)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func askVia(t *testing.T, c *mesh.Client, target, text string) (string, error) {
	t.Helper()
	conn, err := c.DialPeer(target)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, text+"\n")
	line, err := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimSpace(line), err
}

func TestСерверыКакУзлыСети(t *testing.T) {
	addr, sock := startMeshd(t)
	admin := NewAdmin(sock)
	ctx := context.Background()
	if err := admin.SetSelf(ctx, "Франкфурт", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := admin.SetSelf(ctx, "x", true, "80, abc"); err == nil {
		t.Fatal("испорченный список портов принят")
	}

	mainPort := echoOn(t, "главный")
	relayPort := echoOn(t, "побочный")

	link := &ServerLink{Addr: addr, Info: ServerInfo{ID: "de1", Name: "Германия"},
		Allow: func(p int) (bool, string) { return p == relayPort, "" }}
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go link.Run(lctx)

	c := mesh.New(mesh.Config{Addr: addr, Key: mesh.NewKey(), DeviceID: mesh.NewDeviceID(), Name: "телефон"},
		func(n, a string) (net.Conn, error) { return net.Dial(n, a) }, nil)
	go c.Run(lctx)
	waitFor(t, "устройство не увидело оба сервера", func() bool {
		_, a := c.Resolve("frankfurt.mesh")
		_, b := c.Resolve("germaniya.mesh")
		return a && b
	})
	if ip, _ := c.Resolve("frankfurt"); ip.String() != "198.19.255.254" {
		t.Fatalf("адрес главного %s", ip)
	}

	got, err := askVia(t, c, "frankfurt.mesh:"+strconv.Itoa(mainPort), "привет")
	if err != nil || got != "главный: привет" {
		t.Fatalf("главный сервер: %q, %v", got, err)
	}
	got, err = askVia(t, c, "germaniya.mesh:"+strconv.Itoa(relayPort), "привет")
	if err != nil || got != "побочный: привет" {
		t.Fatalf("побочный сервер: %q, %v", got, err)
	}
	if _, err := askVia(t, c, "germaniya.mesh:"+strconv.Itoa(mainPort), "x"); err == nil ||
		!strings.Contains(err.Error(), "не пускает") {
		t.Fatalf("запрещённый порт побочного: %v", err)
	}

	// Главный пускает только на перечисленные порты.
	if err := admin.SetSelf(ctx, "Франкфурт", true, "1-10"); err != nil {
		t.Fatal(err)
	}
	if _, err := askVia(t, c, "frankfurt.mesh:"+strconv.Itoa(mainPort), "x"); err == nil ||
		!strings.Contains(err.Error(), "не пускает") {
		t.Fatalf("запрещённый порт главного: %v", err)
	}

	// Выключили — главный пропадает из сети.
	if err := admin.SetSelf(ctx, "Франкфурт", false, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "выключенный сервер остался в сети", func() bool {
		_, ok := c.Resolve("frankfurt.mesh")
		return !ok
	})
	st, _ := admin.State(ctx)
	if st.Self.Enabled || st.Self.IP != "198.19.255.254" || len(st.Servers) != 1 || st.Servers[0].IP == "" {
		t.Fatalf("состояние: %+v", st)
	}
}

func TestPortAllowed(t *testing.T) {
	for spec, cases := range map[string]map[int]bool{
		"":             {22: true, 65535: true},
		"22, 80":       {22: true, 80: true, 81: false},
		"8000-8100,22": {8050: true, 22: true, 7999: false},
	} {
		for port, want := range cases {
			if PortAllowed(spec, port) != want {
				t.Errorf("PortAllowed(%q, %d) != %v", spec, port, want)
			}
		}
	}
	for _, bad := range []string{"abc", "0", "70000", "90-80", "22,"} {
		if ValidPorts(bad) == nil {
			t.Errorf("ValidPorts(%q) принял", bad)
		}
	}
}
