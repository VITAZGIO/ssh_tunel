package webui

// TestUDPRelayServerSourceMatchesCmd — единственная защита от рассинхрона:
// vpsassets/udprelay_server.go.txt существует лишь потому, что go:embed не
// умеет "../.." в пути и не может встроить sshtunnel/cmd/udprelay/main.go
// напрямую. Это копия, и если кто-то поправит оригинал, не тронув копию (или
// наоборот), сервер, который мастер ставит на VPS, тихо разъедется с тем,
// что собирается и тестируется в этом репозитории. Тест сравнивает их
// побайтово и падает при любом расхождении.

import (
	"os"
	"testing"
)

func TestUDPRelayServerSourceMatchesCmd(t *testing.T) {
	embedded, err := udpRelayServerSource()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("../../cmd/udprelay/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if embedded != string(original) {
		t.Fatal("vpsassets/udprelay_server.go.txt разошёлся с cmd/udprelay/main.go — " +
			"скопируй актуальный main.go поверх vpsassets/udprelay_server.go.txt")
	}
}
