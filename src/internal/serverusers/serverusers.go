// Пакет serverusers — клиентская сторона входа панели для создания
// пользователей из программы (panel/selfservice.go). Общий для программы на
// компьютере и приложения на Android.
//
// Вход слушает 127.0.0.1:47832 на сервере: достаём через туннель, сервер
// узнаёт нас по SSH-пользователю, с которым мы подключены.
package serverusers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Addr — вход панели на сервере (panel.SelfServiceAddr).
const Addr = "127.0.0.1:47832"

// Dial открывает соединение через туннель.
type Dial func(network, addr string) (net.Conn, error)

// Call вызывает вход: op — "whoami" или "create" (body — имя, тип, пароль).
func Call(dial Dial, op string, body any) (map[string]any, error) {
	if op != "whoami" && op != "create" {
		return nil, errors.New("неизвестная операция")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
				return dial(network, Addr)
			},
			DisableKeepAlives: true,
		},
	}
	if body == nil {
		body = struct{}{}
	}
	data, _ := json.Marshal(body)
	resp, err := client.Post("http://"+Addr+"/v1/"+op, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("сервер не ответил: на нём нет панели ssh_tunnel свежей версии "+
			"или её вход закрыт (%v)", shortErr(err))
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("странный ответ сервера (%s)", resp.Status)
	}
	if msg, _ := out["error"].(string); msg != "" {
		return nil, errors.New(msg)
	}
	return out, nil
}

// CreateBody — тело запроса create.
func CreateBody(name, deviceType, password string) map[string]string {
	return map[string]string{"name": name, "deviceType": deviceType, "password": password}
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}
