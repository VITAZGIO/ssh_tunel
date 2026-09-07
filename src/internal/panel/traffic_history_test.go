package panel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrafficHistoryСкладываетДниЗаПериод(t *testing.T) {
	h, err := OpenTrafficHistory(filepath.Join(t.TempDir(), "traffic.json"))
	if err != nil {
		t.Fatalf("OpenTrafficHistory: %v", err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.Local)
	// Три дня: сегодня, вчера и десять дней назад (то есть в этом же месяце,
	// но за пределами недели).
	record := func(day time.Time, rx, tx uint64) {
		if err := h.Record(day, map[string]DayTraffic{"tun_a": {RxBytes: rx, TxBytes: tx}}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	record(now, 100, 10)
	record(now.AddDate(0, 0, -1), 200, 20)
	record(now.AddDate(0, 0, -10), 400, 40)

	cases := []struct {
		period string
		wantRx uint64
	}{
		{"day", 100},
		{"week", 300},  // сегодня + вчера, десятидневной давности уже нет
		{"month", 700}, // всё три дня внутри сентября
	}
	for _, tc := range cases {
		sum := h.SumSince(PeriodStart(tc.period, now))
		if got := sum["tun_a"].RxBytes; got != tc.wantRx {
			t.Errorf("период %q: принято %d, ожидалось %d", tc.period, got, tc.wantRx)
		}
	}
}

func TestTrafficHistoryПереживаетПерезапуск(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	now := time.Now()

	h1, err := OpenTrafficHistory(path)
	if err != nil {
		t.Fatalf("OpenTrafficHistory: %v", err)
	}
	if err := h1.Record(now, map[string]DayTraffic{"tun_a": {RxBytes: 5, TxBytes: 7}}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	h2, err := OpenTrafficHistory(path)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	got := h2.SumSince(PeriodStart("day", now))["tun_a"]
	if got.RxBytes != 5 || got.TxBytes != 7 {
		t.Errorf("после перезапуска %+v, ожидалось {5 7}", got)
	}
}

// Пустой прирост не должен ни попадать в историю, ни заставлять переписывать
// файл: без трафика раз в 15 секунд ничего не меняется.
func TestTrafficHistoryНеЗаписываетПустойПрирост(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	h, err := OpenTrafficHistory(path)
	if err != nil {
		t.Fatalf("OpenTrafficHistory: %v", err)
	}
	if err := h.Record(time.Now(), map[string]DayTraffic{"tun_a": {}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("файл истории создан, хотя записывать было нечего")
	}
	if len(h.Days(time.Time{})) != 0 {
		t.Error("пустой прирост попал в историю")
	}
}

func TestPeriodStartЗаВсёВремяНулевоеВремя(t *testing.T) {
	if got := PeriodStart("all", time.Now()); !got.IsZero() {
		t.Errorf("PeriodStart(all) = %v, ожидалось нулевое время", got)
	}
	if got := PeriodStart("месяц-которого-нет", time.Now()); !got.IsZero() {
		t.Errorf("неизвестный период должен вести себя как «за всё время», получено %v", got)
	}
}

func TestPeriodStartМесяцСПервогоЧисла(t *testing.T) {
	now := time.Date(2026, 9, 15, 23, 59, 0, 0, time.Local)
	got := PeriodStart("month", now)
	want := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("PeriodStart(month) = %v, ожидалось %v", got, want)
	}
}
