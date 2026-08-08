// Package speedtest меряет реальную пропускную способность туннеля.
//
// Это не то же самое, что счётчики трафика на главном экране: те показывают,
// сколько данных идёт прямо сейчас, а тест намеренно загружает канал и
// показывает, на что он вообще способен.
//
// Две вещи, без которых измерение врёт:
//
//   - Несколько параллельных потоков. Одно TCP-соединение упирается в одно
//     окно перегрузки, и на канале с задержкой до Амстердама один поток
//     покажет в разы меньше реального потолка. Потоков берём столько же,
//     сколько соединений в пуле SSH, — так меряется именно то, чем
//     пользуется браузер.
//
//   - Разгон. Первые доли секунды соединение только раскачивается, и если
//     считать с самого начала, результат занижен. Поэтому байты, переданные
//     за время разгона, в расчёт не идут.
package speedtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Публичные точки Cloudflare — те же, что использует их собственный
// измеритель скорости. Отдают и принимают произвольный объём данных.
const (
	DefaultDownURL = "https://speed.cloudflare.com/__down?bytes=1000000000"
	DefaultUpURL   = "https://speed.cloudflare.com/__up"
)

type Options struct {
	// Dial — как открывать соединения. Сюда передаётся дозвон через туннель,
	// иначе тест измерит обычный канал, а не туннель.
	Dial func(network, addr string) (net.Conn, error)

	Streams  int           // сколько потоков лить параллельно
	Duration time.Duration // сколько времени мерить каждое направление
	WarmUp   time.Duration // сколько времени в начале не учитывать

	DownURL string
	UpURL   string

	// OnProgress вызывается по ходу теста: phase — "down" или "up".
	OnProgress func(phase string, mbps float64)
}

type Result struct {
	DownMbps float64 `json:"downMbps"`
	UpMbps   float64 `json:"upMbps"`
}

func (o *Options) applyDefaults() {
	if o.Streams < 1 {
		o.Streams = 4
	}
	if o.Duration <= 0 {
		o.Duration = 8 * time.Second
	}
	if o.WarmUp <= 0 {
		o.WarmUp = time.Second
	}
	if o.DownURL == "" {
		o.DownURL = DefaultDownURL
	}
	if o.UpURL == "" {
		o.UpURL = DefaultUpURL
	}
	if o.WarmUp >= o.Duration {
		o.WarmUp = o.Duration / 4
	}
}

// Run прогоняет оба направления по очереди: сначала приём, затем отдача.
// Одновременно их мерить нельзя — они делят один канал и мешали бы друг другу.
func Run(ctx context.Context, o Options) (Result, error) {
	o.applyDefaults()
	if o.Dial == nil {
		return Result{}, errors.New("не задан способ подключения")
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
				return o.Dial(network, addr)
			},
			// Каждый поток должен идти своим соединением, иначе они
			// выстроятся в очередь и тест покажет скорость одного.
			MaxConnsPerHost:       o.Streams,
			MaxIdleConnsPerHost:   o.Streams,
			ResponseHeaderTimeout: 20 * time.Second,
			DisableCompression:    true,
		},
	}
	defer client.CloseIdleConnections()

	var res Result
	var err error

	res.DownMbps, err = o.measure(ctx, "down", func(ctx context.Context, counted *atomic.Int64) error {
		return download(ctx, client, o.DownURL, counted)
	})
	if err != nil {
		return res, err
	}

	res.UpMbps, err = o.measure(ctx, "up", func(ctx context.Context, counted *atomic.Int64) error {
		return upload(ctx, client, o.UpURL, counted)
	})
	return res, err
}

// measure запускает потоки, ждёт разгон, считает переданное за оставшееся
// время и переводит в мегабиты в секунду.
func (o *Options) measure(ctx context.Context, phase string, stream func(context.Context, *atomic.Int64) error) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, o.Duration+15*time.Second)
	defer cancel()

	var counted atomic.Int64
	var firstErr atomic.Value
	var wg sync.WaitGroup

	streamCtx, stopStreams := context.WithCancel(ctx)
	defer stopStreams()

	for i := 0; i < o.Streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := stream(streamCtx, &counted); err != nil {
				if streamCtx.Err() == nil { // не наша же остановка
					firstErr.CompareAndSwap(nil, err)
				}
			}
		}()
	}

	// Разгон: ждём и обнуляем точку отсчёта.
	select {
	case <-time.After(o.WarmUp):
	case <-ctx.Done():
	}
	start := time.Now()
	base := counted.Load()

	// Пока идёт замер, сообщаем промежуточный результат.
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(o.Duration - o.WarmUp)

loop:
	for {
		select {
		case <-ticker.C:
			if o.OnProgress != nil {
				o.OnProgress(phase, mbps(counted.Load()-base, time.Since(start)))
			}
		case <-deadline:
			break loop
		case <-ctx.Done():
			break loop
		}
	}

	total := counted.Load() - base
	elapsed := time.Since(start)
	stopStreams()
	wg.Wait()

	if total == 0 {
		if e, ok := firstErr.Load().(error); ok && e != nil {
			return 0, fmt.Errorf("тест не удался: %w", e)
		}
		return 0, errors.New("данные не пошли — проверь соединение")
	}
	v := mbps(total, elapsed)
	if o.OnProgress != nil {
		o.OnProgress(phase, v)
	}
	return v, nil
}

func mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

func download(ctx context.Context, client *http.Client, url string, counted *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("сервер измерения ответил %s", resp.Status)
	}
	_, err = io.Copy(counter{counted}, resp.Body)
	return err
}

func upload(ctx context.Context, client *http.Client, url string, counted *atomic.Int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &generator{ctx: ctx, counted: counted})
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("сервер измерения ответил %s", resp.Status)
	}
	return nil
}

// counter считает прочитанное, сами данные выбрасывает.
type counter struct{ n *atomic.Int64 }

func (c counter) Write(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), nil
}

// generator отдаёт данные, пока тест не закончится. Байты не нулевые: нули
// слишком хорошо сжимаются, и на пути со сжатием результат оказался бы
// завышенным.
type generator struct {
	ctx     context.Context
	counted *atomic.Int64
	seed    uint32
}

func (g *generator) Read(p []byte) (int, error) {
	if err := g.ctx.Err(); err != nil {
		return 0, io.EOF
	}
	if g.seed == 0 {
		g.seed = 0x9e3779b9
	}
	for i := range p {
		g.seed ^= g.seed << 13
		g.seed ^= g.seed >> 17
		g.seed ^= g.seed << 5
		p[i] = byte(g.seed)
	}
	g.counted.Add(int64(len(p)))
	return len(p), nil
}
