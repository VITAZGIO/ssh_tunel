package tunnel

// Проверка SOCKS5 UDP ASSOCIATE целиком: настоящий (тестовый) SSH-сервер,
// настоящий бинарник ретранслятора и минимальный клиент SOCKS5 UDP —
// как повёл бы себя браузер или игра, попроси она прокси об UDP.

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestSOCKS5UDPAssociateСквозьРетранслятор(t *testing.T) {
	relayAddr := startUDPRelayProcess(t)
	target := echoUDP(t)

	tun, socksAddr, _, _ := startTunnel(t, 1)
	tun.cfg.UDPRelayEnabled = true
	tun.cfg.UDPRelayAddr = relayAddr
	if !tun.WaitReady(1, 5*time.Second) {
		t.Fatal("пул не поднялся")
	}

	control, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	// Рукопожатие SOCKS5: без аутентификации.
	if _, err := control.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	hello := make([]byte, 2)
	if _, err := io.ReadFull(control, hello); err != nil {
		t.Fatal(err)
	}
	if hello[0] != 0x05 || hello[1] != 0x00 {
		t.Fatalf("рукопожатие не удалось: % x", hello)
	}

	// UDP ASSOCIATE: свой адрес не знаем — 0.0.0.0:0, как обычно и делают.
	req := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := control.Write(req); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(control, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("ASSOCIATE отказал: % x", reply)
	}
	relayUDPAddr := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:10]))}

	appSocket, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer appSocket.Close()

	// Датаграмма приложения в обёртке SOCKS5 UDP: RSV RSV FRAG ATYP ADDR PORT DATA.
	pkt := make([]byte, 0, 10+16)
	pkt = append(pkt, 0, 0, 0, 0x01)
	pkt = append(pkt, target.IP.To4()...)
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(target.Port))
	pkt = append(pkt, portBuf[:]...)
	pkt = append(pkt, []byte("привет по UDP")...)

	if _, err := appSocket.WriteToUDP(pkt, relayUDPAddr); err != nil {
		t.Fatal(err)
	}

	appSocket.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	n, from, err := appSocket.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("ответ не пришёл: %v", err)
	}
	if !from.IP.Equal(relayUDPAddr.IP) || from.Port != relayUDPAddr.Port {
		t.Errorf("ответ пришёл не от локального UDP-адреса ассоциации: %v", from)
	}
	host, port, data, err := decodeSOCKS5UDP(buf[:n])
	if err != nil {
		t.Fatalf("не разобрал ответ: %v", err)
	}
	if port != uint16(target.Port) || string(data) != "привет по UDP" {
		t.Fatalf("получено host=%s port=%d data=%q, ожидался ответ от %s с тем же текстом",
			host, port, data, target)
	}

	// Закрываем управляющее соединение — по RFC 1928 это и завершает
	// ассоциацию; после этого прокси должен освободить локальный UDP-сокет.
	control.Close()
}
