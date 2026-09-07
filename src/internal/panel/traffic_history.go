package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TrafficHistory — трафик клиентов, разложенный по дням.
//
// Сами счётчики в Client (RxBytes/TxBytes) только растут и знают лишь одно
// число «за всё время» — по ним нельзя ответить на вопрос «сколько за этот
// месяц». Поэтому прирост, который SyncTraffic и так вычисляет на каждом
// обходе, попутно складывается ещё и сюда, в корзину текущего дня. Дни —
// достаточно мелкое деление: любой период, который показывает панель
// (сегодня, неделя, месяц), собирается сложением дней, а файл остаётся
// маленьким даже за годы.
//
// Хранится рядом с clients.json одним JSON-файлом — как и всё остальное в
// панели, без базы данных: десятки клиентов и сотни дней, а не миллионы
// записей.
type TrafficHistory struct {
	path string

	mu   sync.Mutex
	days map[string]map[string]DayTraffic
}

// DayTraffic — сколько байт клиент принял и отдал за один день.
type DayTraffic struct {
	RxBytes uint64 `json:"rx"`
	TxBytes uint64 `json:"tx"`
}

// historyRetentionDays — сколько дней держим. Год с запасом: за «всё время»
// панель всё равно отвечает по вечным счётчикам самих клиентов, а не по
// истории, так что старые дни нужны только для графика и выборок.
const historyRetentionDays = 400

// dayKey — день как "2006-01-02" по местному времени сервера. Именно
// местному: человек, который смотрит «за сегодня», имеет в виду свой день,
// а не UTC-сутки.
func dayKey(t time.Time) string { return t.Format("2006-01-02") }

func OpenTrafficHistory(path string) (*TrafficHistory, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("не могу создать папку для %s: %w", path, err)
	}
	h := &TrafficHistory{path: path, days: map[string]map[string]DayTraffic{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не могу прочитать %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return h, nil
	}
	var stored map[string]map[string]DayTraffic
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("не могу разобрать %s: %w", path, err)
	}
	if stored != nil {
		h.days = stored
	}
	return h, nil
}

// Record добавляет прирост за один обход к корзине дня now. Пустые приросты
// не сохраняются вовсе: раз в 15 секунд без трафика ничего не изменилось, и
// перезаписывать из-за этого файл незачем.
func (h *TrafficHistory) Record(now time.Time, deltas map[string]DayTraffic) error {
	if h == nil || len(deltas) == 0 {
		return nil
	}
	changed := false
	key := dayKey(now)

	h.mu.Lock()
	for id, d := range deltas {
		if d.RxBytes == 0 && d.TxBytes == 0 {
			continue
		}
		if h.days[key] == nil {
			h.days[key] = map[string]DayTraffic{}
		}
		cur := h.days[key][id]
		cur.RxBytes += d.RxBytes
		cur.TxBytes += d.TxBytes
		h.days[key][id] = cur
		changed = true
	}
	if changed {
		h.pruneLocked(now)
	}
	h.mu.Unlock()

	if !changed {
		return nil
	}
	return h.save()
}

// pruneLocked выбрасывает дни старше historyRetentionDays. Вызывается уже
// под замком.
func (h *TrafficHistory) pruneLocked(now time.Time) {
	cutoff := dayKey(now.AddDate(0, 0, -historyRetentionDays))
	for key := range h.days {
		if key < cutoff { // формат YYYY-MM-DD сравнивается как строка
			delete(h.days, key)
		}
	}
}

// SumSince складывает дни, начиная с указанного (включительно). Возвращает
// сумму по каждому клиенту — общий итог вызывающий код считает сам, чтобы не
// ходить по истории дважды.
func (h *TrafficHistory) SumSince(since time.Time) map[string]DayTraffic {
	out := map[string]DayTraffic{}
	if h == nil {
		return out
	}
	from := dayKey(since)

	h.mu.Lock()
	defer h.mu.Unlock()
	for key, byClient := range h.days {
		if key < from {
			continue
		}
		for id, d := range byClient {
			cur := out[id]
			cur.RxBytes += d.RxBytes
			cur.TxBytes += d.TxBytes
			out[id] = cur
		}
	}
	return out
}

// Days отдаёт дни за период по возрастанию — для графика в панели.
func (h *TrafficHistory) Days(since time.Time) []DayPoint {
	if h == nil {
		return nil
	}
	from := dayKey(since)

	h.mu.Lock()
	defer h.mu.Unlock()
	var out []DayPoint
	for key, byClient := range h.days {
		if key < from {
			continue
		}
		var p DayPoint
		p.Day = key
		for _, d := range byClient {
			p.RxBytes += d.RxBytes
			p.TxBytes += d.TxBytes
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

// DayPoint — сумма по всем клиентам за один день.
type DayPoint struct {
	Day     string `json:"day"`
	RxBytes uint64 `json:"rxBytes"`
	TxBytes uint64 `json:"txBytes"`
}

func (h *TrafficHistory) save() error {
	h.mu.Lock()
	data, err := json.Marshal(h.days)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

// PeriodStart — начало периода, который выбирают в панели. Неизвестное имя
// (и "all") даёт нулевое время: «за всё время».
func PeriodStart(period string, now time.Time) time.Time {
	y, m, d := now.Date()
	loc := now.Location()
	switch period {
	case "day":
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	case "week":
		// Последние 7 дней включая сегодня — так понятнее, чем «с
		// понедельника»: в понедельник утром такая неделя не обнуляется.
		return time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, -6)
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, loc)
	default:
		return time.Time{}
	}
}
