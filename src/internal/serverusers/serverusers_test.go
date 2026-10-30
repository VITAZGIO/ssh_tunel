package serverusers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestВызовВхода(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("тип: %q", r.Header.Get("Content-Type"))
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/v1/create" && body["password"] == "верный":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "config": "{}"})
		case r.URL.Path == "/v1/create":
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "неверный пароль"})
		default:
			json.NewEncoder(w).Encode(map[string]any{"name": "ПК", "canCreate": true})
		}
	}))
	defer srv.Close()
	var dialed string
	dial := func(network, addr string) (net.Conn, error) {
		dialed = addr
		return net.Dial(network, srv.Listener.Addr().String())
	}
	out, err := Call(dial, "whoami", nil)
	if err != nil || out["canCreate"] != true || dialed != Addr {
		t.Fatalf("whoami: %v %v %q", out, err, dialed)
	}
	if _, err := Call(dial, "create", CreateBody("a", "android", "нет")); err == nil || err.Error() != "неверный пароль" {
		t.Fatalf("ждали «неверный пароль», получили %v", err)
	}
	if out, err := Call(dial, "create", CreateBody("a", "android", "верный")); err != nil || out["config"] != "{}" {
		t.Fatalf("create: %v %v", out, err)
	}
	if _, err := Call(dial, "drop", nil); err == nil {
		t.Fatal("пропустили неизвестную операцию")
	}
}
