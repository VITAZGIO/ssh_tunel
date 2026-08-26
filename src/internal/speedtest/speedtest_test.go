package speedtest

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Тесты гоняют измерение через локальный сервер: проверяется, что счёт байтов
// и перевод в мегабиты работают, что параллельные потоки складываются, а не
// теряются, и что ошибки не выдаются за нулевую скорость.

func testServer(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 32*1024)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	})
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "нет", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func directDial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, 5*time.Second)
}

func TestMeasuresBothDirections(t *testing.T) {
	base := testServer(t)

	var phases []string
	res, err := Run(context.Background(), Options{
		Dial:     directDial,
		Streams:  2,
		Duration: 900 * time.Millisecond,
		WarmUp:   150 * time.Millisecond,
		DownURLs: []string{base + "/down"},
		UpURLs:   []string{base + "/up"},
		OnProgress: func(phase string, mbps float64) {
			phases = append(phases, phase)
		},
	})
	if err != nil {
		t.Fatalf("тест скорости не прошёл: %v", err)
	}
	if res.DownMbps <= 0 {
		t.Fatalf("приём измерен как %v — счётчик не работает", res.DownMbps)
	}
	if res.UpMbps <= 0 {
		t.Fatalf("отдача измерена как %v — счётчик не работает", res.UpMbps)
	}
	// По петлевому адресу скорость заведомо больше сотни мегабит; если вышло
	// меньше, значит время или байты считаются неправильно.
	if res.DownMbps < 50 {
		t.Fatalf("подозрительно низкий приём по локальному адресу: %.1f Мбит/с", res.DownMbps)
	}

	var sawDown, sawUp bool
	for _, p := range phases {
		sawDown = sawDown || p == "down"
		sawUp = sawUp || p == "up"
	}
	if !sawDown || !sawUp {
		t.Fatalf("ход теста не сообщался по обоим направлениям: %v", phases)
	}
}

// Несколько потоков должны складываться: если бы они вставали в очередь на
// одном соединении, результат не рос бы вовсе.
func TestParallelStreamsAddUp(t *testing.T) {
	base := testServer(t)

	run := func(streams int) float64 {
		res, err := Run(context.Background(), Options{
			Dial:     directDial,
			Streams:  streams,
			Duration: 800 * time.Millisecond,
			WarmUp:   150 * time.Millisecond,
			DownURLs: []string{base + "/down"},
			UpURLs:   []string{base + "/up"},
		})
		if err != nil {
			t.Fatalf("%d поток(ов): %v", streams, err)
		}
		return res.DownMbps
	}

	one, four := run(1), run(4)
	t.Logf("один поток %.0f Мбит/с, четыре потока %.0f Мбит/с", one, four)
	if four < one {
		t.Fatalf("четыре потока (%.0f) медленнее одного (%.0f) — потоки не параллелятся", four, one)
	}
}

// Недоступный сервер должен давать ошибку, а не «скорость 0».
func TestReportsErrorInsteadOfZero(t *testing.T) {
	base := testServer(t)

	_, err := Run(context.Background(), Options{
		Dial:     directDial,
		Streams:  1,
		Duration: 500 * time.Millisecond,
		WarmUp:   100 * time.Millisecond,
		DownURLs: []string{base + "/fail"},
		UpURLs:   []string{base + "/up"},
	})
	if err == nil {
		t.Fatal("ошибка сервера измерения выдана за успешный тест")
	}
}

func TestNeedsDial(t *testing.T) {
	if _, err := Run(context.Background(), Options{}); err == nil {
		t.Fatal("тест без способа подключения должен возвращать ошибку")
	}
}

// Первый адрес может отвечать отказом — тогда берётся следующий рабочий.
// Именно этого не хватало, когда Cloudflare начал отдавать 403.
func TestFallsBackToWorkingServer(t *testing.T) {
	base := testServer(t)

	res, err := Run(context.Background(), Options{
		Dial:     directDial,
		Streams:  2,
		Duration: 700 * time.Millisecond,
		WarmUp:   120 * time.Millisecond,
		DownURLs: []string{base + "/fail", base + "/down"},
		UpURLs:   []string{base + "/up"},
	})
	if err != nil {
		t.Fatalf("перебор адресов не сработал: %v", err)
	}
	if res.DownMbps <= 0 {
		t.Fatal("приём не измерен, хотя рабочий адрес в списке был")
	}
}

// Отдачу принимают немногие сервисы. Если ни один не отвечает, уже измеренный
// приём выбрасывать нельзя — пользователю нужна хотя бы половина результата.
func TestUploadFailureKeepsDownload(t *testing.T) {
	base := testServer(t)

	res, err := Run(context.Background(), Options{
		Dial:     directDial,
		Streams:  1,
		Duration: 700 * time.Millisecond,
		WarmUp:   120 * time.Millisecond,
		DownURLs: []string{base + "/down"},
		UpURLs:   []string{base + "/fail"},
	})
	if err != nil {
		t.Fatalf("неудача с отдачей не должна ронять весь тест: %v", err)
	}
	if res.DownMbps <= 0 {
		t.Fatal("приём потерян из-за неудачи с отдачей")
	}
	if res.UpMbps != 0 {
		t.Fatalf("отдача измерена как %v, хотя сервер отказал", res.UpMbps)
	}
	if res.Note == "" {
		t.Fatal("про неудачу с отдачей ничего не сказано")
	}
	t.Logf("приём %.0f Мбит/с, пометка: %s", res.DownMbps, res.Note)
}

// Замер мимо туннеля — отдельная фаза с отдельной цифрой. Без неё результат
// теста не с чем сравнить, ради чего сравнение и добавлено.
func TestDirectComparisonRuns(t *testing.T) {
	base := testServer(t)

	var sawDirect bool
	res, err := Run(context.Background(), Options{
		Dial:       directDial,
		DirectDial: directDial,
		Streams:    2,
		Duration:   700 * time.Millisecond,
		WarmUp:     150 * time.Millisecond,
		DownURLs:   []string{base + "/down"},
		UpURLs:     []string{base + "/up"},
		OnProgress: func(phase string, mbps float64) {
			sawDirect = sawDirect || phase == PhaseDirect
		},
	})
	if err != nil {
		t.Fatalf("тест скорости не прошёл: %v", err)
	}
	if !sawDirect {
		t.Fatal("фаза замера мимо туннеля не сообщалась")
	}
	if res.DirectDownMbps <= 0 {
		t.Fatalf("прямой приём измерен как %v", res.DirectDownMbps)
	}
	if res.Verdict == "" {
		t.Fatal("сравнение есть, а вывода из него нет")
	}
}

// Без DirectDial поведение прежнее: лишней фазы нет и вывода тоже.
func TestDirectComparisonOptional(t *testing.T) {
	base := testServer(t)

	res, err := Run(context.Background(), Options{
		Dial:     directDial,
		Streams:  2,
		Duration: 500 * time.Millisecond,
		WarmUp:   100 * time.Millisecond,
		DownURLs: []string{base + "/down"},
		UpURLs:   []string{base + "/up"},
	})
	if err != nil {
		t.Fatalf("тест скорости не прошёл: %v", err)
	}
	if res.DirectDownMbps != 0 || res.Verdict != "" {
		t.Fatalf("сравнения не просили, а оно есть: %.1f %q", res.DirectDownMbps, res.Verdict)
	}
}

func TestVerdictThresholds(t *testing.T) {
	cases := []struct {
		name           string
		tunnel, direct float64
		want           string
	}{
		{"нечего сравнивать", 50, 0, ""},
		{"туннель не мешает", 90, 100, "туннель почти не мешает — это потолок самого интернета"},
		{"половина канала", 50, 100, "туннель забирает примерно половину — обычная плата за шифрование и лишний путь"},
		{"узкое место сервер", 1, 100, "напрямую канал заметно быстрее: узкое место — сервер или путь до него, а не провайдер"},
	}
	for _, c := range cases {
		if got := verdict(c.tunnel, c.direct); got != c.want {
			t.Errorf("%s: получили %q, ждали %q", c.name, got, c.want)
		}
	}
}
