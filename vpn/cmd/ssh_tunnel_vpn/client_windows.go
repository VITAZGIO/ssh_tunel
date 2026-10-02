package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/events"
	"sshtunnel/internal/nativeui"
	"sshtunnel/internal/updater"
	"sshtunnel/internal/webui"
	"sshtunnel/vpn/internal/winsvc"
)

// Окно службы. Ядро работает в службе Windows, а это — обычная программа без
// прав администратора: показывает интерфейс службы (через winsvc.Proxy) и
// значок в трее. Галочку «Запускать при старте системы» тоже решает окно:
// снять службу можно только с правами администратора, через окно UAC.

type client struct {
	p  *winsvc.Proxy
	hc *http.Client
}

func runClient(tray, noWindow bool) {
	if !winsvc.Running() {
		// Служба стоит, но не работает (остановили или упала). Права на
		// запуск у вошедшего пользователя есть — см. winsvc.serviceDACL.
		if err := winsvc.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "запуск службы:", err)
		}
	}

	dir := config.Dir()
	c := &client{
		p: winsvc.NewProxy(dir, map[string]http.HandlerFunc{
			// У службы нет рабочего стола — диалоги открывает окно.
			"/api/pickfile":     webui.HandlePickFile,
			"/api/openterminal": webui.HandleOpenTerminal,
			"/api/bootstart":    webui.HandleBootStart,
		}),
		hc: &http.Client{Timeout: 5 * time.Second},
	}
	ep, err := c.waitEndpoint(30 * time.Second)
	if err != nil {
		fatal("Служба ssh_tunnel VPN не отвечает.\n\n" +
			"Проверь её в «Службах» Windows (services.msc) и журнал службы:\n" +
			filepath.Join(dir, "service.log") + "\n\n" + err.Error())
	}
	c.offerServiceUpdate(ep)
	webui.SetBootControl(clientBoot{})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("Не удалось запустить интерфейс: " + err.Error())
	}
	go (&http.Server{Handler: c.p, ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
	pageURL := fmt.Sprintf("http://%s/?t=%s", ln.Addr(), c.p.Token)
	if noWindow {
		fmt.Println(pageURL)
		select {}
	}

	go c.trayStatus()
	// «Выход» из трея закрывает только окно: VPN и сеть устройств — дело
	// службы, они продолжают работать.
	runWindow(pageURL, tray, c.running, c.toggle, func() {})
}

// waitEndpoint ждёт, пока служба запишет адрес и начнёт отвечать. Файл от
// прошлого запуска мог остаться (служба упала) — поэтому не только читаем,
// но и спрашиваем саму службу.
func (c *client) waitEndpoint(timeout time.Duration) (winsvc.Endpoint, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		c.p.Forget()
		ep := c.p.Endpoint()
		if ep.Addr != "" {
			_, err := c.call(ep, http.MethodGet, "/api/status")
			if err == nil {
				return ep, nil
			}
			lastErr = err
		} else {
			lastErr = errors.New("служба ещё не записала свой адрес")
		}
		if time.Now().After(deadline) {
			return winsvc.Endpoint{}, lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// call — запрос к службе от имени окна (для трея).
func (c *client) call(ep winsvc.Endpoint, method, path string) ([]byte, error) {
	body := bytes.NewReader(nil)
	if method == http.MethodPost {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequest(method, "http://"+ep.Addr+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Token", ep.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("служба ответила %s", resp.Status)
	}
	return buf.Bytes(), nil
}

// running и toggle — для меню у значка в трее.
func (c *client) running() bool {
	data, err := c.call(c.p.Endpoint(), http.MethodGet, "/api/status")
	if err != nil {
		return false
	}
	var st struct {
		Running bool `json:"running"`
	}
	json.Unmarshal(data, &st)
	return st.Running
}

func (c *client) toggle() {
	path := "/api/start"
	if c.running() {
		path = "/api/stop"
	}
	c.call(c.p.Endpoint(), http.MethodPost, path)
}

// trayStatus держит подпись у значка в трее в актуальном состоянии — по
// потоку событий службы. Служба перезапустилась — переподключаемся.
func (c *client) trayStatus() {
	for {
		ep := c.p.Endpoint()
		if ep.Addr != "" {
			c.readEvents(ep)
		}
		c.p.Forget()
		time.Sleep(3 * time.Second)
	}
}

func (c *client) readEvents(ep winsvc.Endpoint) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+ep.Addr+"/events?t="+url.QueryEscape(ep.Token), nil)
	if err != nil {
		return
	}
	resp, err := (&http.Client{}).Do(req) // без таймаута: поток бесконечный
	if err != nil {
		nativeui.SetStatus("служба не отвечает")
		return
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e events.Event
		if json.Unmarshal([]byte(line), &e) != nil || e.Kind != events.KindState {
			continue
		}
		if name, ok := trayNames[e.State]; ok {
			nativeui.SetStatus(name)
		}
	}
}

// offerServiceUpdate — запустили файл новой версии, а служба работает на
// старой (она запускает свою копию из Program Files). Предлагаем обновить:
// иначе скачанное обновление так и не заработало бы.
func (c *client) offerServiceUpdate(ep winsvc.Endpoint) {
	self, err := os.Executable()
	if err != nil || winsvc.SamePath(self, winsvc.InstalledExe()) {
		return
	}
	if updater.Version == "dev" || ep.Version == updater.Version {
		return
	}
	msg := fmt.Sprintf("Служба ssh_tunnel VPN работает на версии %s, а этот файл — версии %s.\n\n"+
		"Обновить службу? Windows попросит права администратора; VPN на несколько секунд переподключится.",
		ep.Version, updater.Version)
	if showMessage(msg, mbYesNo|mbIconQuestion) != idYes {
		return
	}
	code, err := winsvc.RunElevated(self, []string{"-install-service"}, true)
	if err != nil || code != 0 {
		return
	}
	c.waitEndpoint(30 * time.Second)
}

// clientBoot — галочка «Запускать при старте системы» в окне службы.
// Включена, пока стоит служба. Выключение удаляет службу (через окно UAC) и
// перезапускает программу уже без неё.
type clientBoot struct{}

func (clientBoot) Enabled() bool { return winsvc.Installed() }

func (clientBoot) Set(enable bool) error {
	if enable {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := winsvc.RunElevated(self, []string{"-uninstall-service", "-relaunch"}, false); err != nil {
		return err
	}
	// Удаляющая копия откроет окно заново, когда эта выйдет.
	go func() {
		time.Sleep(500 * time.Millisecond)
		nativeui.Quit()
	}()
	return nil
}
