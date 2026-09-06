package app

// Замер отклика серверов: сколько времени занимает установка TCP-соединения
// с портом SSH. Используется автовыбором самого быстрого сервера и переходом
// на запасной (см. failover.go) — оба решают, куда подключаться, ещё до
// SSH-рукопожатия, поэтому меряется именно TCP-connect, а не что-то глубже.

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"sshtunnel/internal/config"
)

// latencyAttempts — несколько попыток на случай единичной потери пакета:
// берём лучшую, а не первую подвернувшуюся.
const latencyAttempts = 3

// latencyTimeout — предел на одну попытку. Хосты специально ждут ответ
// самого сервера SSH, а не хотя бы TCP handshake где-то на полпути, поэтому
// срок небольшой: если за две секунды порт не открылся, дальше ждать нет
// смысла — есть и другие серверы для сравнения.
const latencyTimeout = 2 * time.Second

// measureLatency — лучшее из нескольких TCP-подключений к addr. Ошибка,
// только если не удалась ни одна попытка.
func measureLatency(ctx context.Context, addr string) (time.Duration, error) {
	var best time.Duration
	var lastErr error
	for i := 0; i < latencyAttempts; i++ {
		d := net.Dialer{Timeout: latencyTimeout}
		start := time.Now()
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			continue
		}
		took := time.Since(start)
		conn.Close()
		if best == 0 || took < best {
			best = took
		}
	}
	if best == 0 {
		return 0, lastErr
	}
	return best, nil
}

// ProfileLatency — результат замера одного сервера.
type ProfileLatency struct {
	ID    string        `json:"id"`
	Ms    int64         `json:"ms"`
	Error string        `json:"error,omitempty"`
	dur   time.Duration // не экспортируется — только для сортировки
}

// measureAllLatencies меряет все профили параллельно.
func measureAllLatencies(ctx context.Context, profiles []config.Profile) []ProfileLatency {
	out := make([]ProfileLatency, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		wg.Add(1)
		go func(i int, p config.Profile) {
			defer wg.Done()
			addr := net.JoinHostPort(p.Host, strconv.Itoa(p.SSHPort))
			d, err := measureLatency(ctx, addr)
			r := ProfileLatency{ID: p.ID, dur: d}
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Ms = d.Milliseconds()
			}
			out[i] = r
		}(i, p)
	}
	wg.Wait()
	return out
}

// rankByLatency сортирует профили по замеренному отклику — быстрее первым,
// ответившие впереди неответивших. Порядок при равном результате (например,
// когда все профили недоступны) — как в исходном списке, чтобы поведение
// оставалось предсказуемым.
func rankByLatency(profiles []config.Profile, results []ProfileLatency) []config.Profile {
	byID := make(map[string]ProfileLatency, len(results))
	for _, r := range results {
		byID[r.ID] = r
	}
	ranked := make([]config.Profile, len(profiles))
	copy(ranked, profiles)
	sort.SliceStable(ranked, func(i, j int) bool {
		ri, oki := byID[ranked[i].ID]
		rj, okj := byID[ranked[j].ID]
		iFailed := !oki || ri.Error != ""
		jFailed := !okj || rj.Error != ""
		if iFailed != jFailed {
			return !iFailed // ответившие впереди неответивших
		}
		if iFailed {
			return false // оба не ответили — порядок не трогаем
		}
		return ri.dur < rj.dur
	})
	return ranked
}

// LatencyReport меряет отклик всех серверов — для отображения рядом с
// вкладками, не для решения о подключении.
func (a *App) LatencyReport(ctx context.Context) []ProfileLatency {
	a.mu.Lock()
	profiles := a.cfg.Profiles
	a.mu.Unlock()
	return measureAllLatencies(ctx, profiles)
}
