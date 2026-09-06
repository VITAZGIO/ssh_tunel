package udprelay

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameRoundTripIPv4(t *testing.T) {
	want := Frame{SessionID: 42, Host: "203.0.113.9", Port: 53, Data: []byte("привет")}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	assertFrameEqual(t, got, want)
}

func TestFrameRoundTripIPv6(t *testing.T) {
	want := Frame{SessionID: 7, Host: "2001:db8::1", Port: 443, Data: []byte{1, 2, 3}}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	assertFrameEqual(t, got, want)
}

func TestFrameRoundTripDomain(t *testing.T) {
	want := Frame{SessionID: 1, Host: "stun.example.com", Port: 3478, Data: []byte("датаграмма")}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	assertFrameEqual(t, got, want)
}

func TestFrameRoundTripFIN(t *testing.T) {
	want := Frame{SessionID: 99, FIN: true}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != want.SessionID || !got.FIN {
		t.Fatalf("получилось %+v, ожидалось %+v", got, want)
	}
}

// Несколько кадров подряд в одном потоке — обычная ситуация при
// мультиплексировании сессий: ReadFrame не должен захватывать байты соседних
// кадров.
func TestMultipleFramesInOneStream(t *testing.T) {
	var buf bytes.Buffer
	frames := []Frame{
		{SessionID: 1, Host: "1.2.3.4", Port: 1, Data: []byte("a")},
		{SessionID: 2, Host: "5.6.7.8", Port: 2, Data: []byte("bb")},
		{SessionID: 1, FIN: true},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("кадр %d: %v", i, err)
		}
		if want.FIN {
			if !got.FIN || got.SessionID != want.SessionID {
				t.Fatalf("кадр %d: получилось %+v, ожидалось %+v", i, got, want)
			}
			continue
		}
		assertFrameEqual(t, got, want)
	}
}

// Слишком большое тело кадра не пишется вовсе — не должно тихо обрезаться.
func TestWriteFrameRejectsOversized(t *testing.T) {
	f := Frame{SessionID: 1, Host: "1.2.3.4", Port: 1, Data: make([]byte, MaxFrameBody+1)}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, f); err == nil {
		t.Fatal("ожидалась ошибка на кадр сверх предела")
	}
	if buf.Len() != 0 {
		t.Error("часть кадра сверх предела всё равно записалась")
	}
}

// Битый заголовок длины (заявлено больше предела) отклоняется, не пытаясь
// выделить память под что попало.
func TestReadFrameRejectsOversizedLength(t *testing.T) {
	var hdr [4]byte
	hdr[0] = 0xFF // заведомо больше MaxFrameBody
	r := bytes.NewReader(hdr[:])
	if _, err := ReadFrame(r); err == nil {
		t.Fatal("ожидалась ошибка на завышенную длину кадра")
	}
}

// Обрыв потока посреди кадра — обычная ошибка ввода-вывода, а не паника.
func TestReadFrameHandlesTruncatedStream(t *testing.T) {
	want := Frame{SessionID: 1, Host: "1.2.3.4", Port: 1, Data: []byte("данные")}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, want); err != nil {
		t.Fatal(err)
	}
	truncated := buf.Bytes()[:buf.Len()-2]
	if _, err := ReadFrame(bytes.NewReader(truncated)); err == nil {
		t.Fatal("ожидалась ошибка на обрезанный поток")
	}
}

func assertFrameEqual(t *testing.T, got, want Frame) {
	t.Helper()
	if got.SessionID != want.SessionID {
		t.Errorf("SessionID = %d, ожидался %d", got.SessionID, want.SessionID)
	}
	if got.FIN != want.FIN {
		t.Errorf("FIN = %v, ожидался %v", got.FIN, want.FIN)
	}
	if !strings.EqualFold(got.Host, want.Host) {
		t.Errorf("Host = %q, ожидался %q", got.Host, want.Host)
	}
	if got.Port != want.Port {
		t.Errorf("Port = %d, ожидался %d", got.Port, want.Port)
	}
	if !bytes.Equal(got.Data, want.Data) {
		t.Errorf("Data = %q, ожидалось %q", got.Data, want.Data)
	}
}
