// Учёт трафика клиентов панели через nftables (ТЗ-09). Идея: своя таблица
// "inet ssh_tunnel_panel" с двумя цепочками — "acct_in" (hook input) и
// "acct_out" (hook output) — и по одному правилу на клиента в каждой,
// подобранному по владельцу сокета (meta skuid) и помеченному комментарием
// с id клиента. Чтение — одной командой "nft -j list ruleset" и разбором
// JSON, без разбора текстового вывода.
//
// Формат JSON, который отдаёт "nft -j list ruleset", нигде официально не
// документирован построчно — предположения ниже основаны на реальных
// выгрузках nftables (см. ParseNFTRules) и проверены только тестами на
// собранных вручную фрагментах этого формата, а не на живом сервере: это
// явно помечено как то, что нужно свериться на настоящем Debian/Ubuntu
// перед тем, как полагаться на цифры трафика в проде (см. docs/PANEL_SETUP.md).
package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	nftTable  = "ssh_tunnel_panel"
	nftFamily = "inet"
	chainIn   = "acct_in"
	chainOut  = "acct_out"
)

// RawCounter — то, что фактически лежит в счётчике nftables прямо сейчас:
// абсолютное значение с последнего сброса (перезапуск nftables, "nft flush",
// пересоздание таблицы), а не накопленный со временем итог. Накоплением
// занимается AccumulateCounter — сырые числа сами по себе, без прошлого
// значения, не говорят, сколько трафика было "с начала времён".
type RawCounter struct {
	RxBytes, RxPackets uint64
	TxBytes, TxPackets uint64
}

// ruleCounter — то, что фактически лежит в поле "counter" одного правила
// nft: пара пакеты/байты без разделения на приём и отдачу — направление
// решает не счётчик, а то, в какой цепочке (chainIn/chainOut) стоит
// правило. Разделение на RxBytes/TxBytes появляется только на уровне
// RawCounter, когда countersByClient сводит правила из обеих цепочек в один
// результат по клиенту.
type ruleCounter struct {
	Packets, Bytes uint64
}

// NFTRule — одно правило из "nft -j list ruleset", в объёме, который нужен
// панели: в какой оно цепочке, чем помечено и что насчитал его счётчик.
// Handle нужен, чтобы потом удалить именно это правило командой
// "nft delete rule ... handle N" — по содержимому правило не удалить, только
// по числовому handle, который nft присваивает при добавлении.
type NFTRule struct {
	Chain   string
	Handle  int
	Comment string
	Counter *ruleCounter // nil, если у правила нет счётчика
}

// TrafficAccountant — всё, что панель делает с nftables. Отдельный
// интерфейс по тому же принципу, что и Provisioner (provision.go): бизнес-
// логика в client_manager.go проверяется тестами с поддельной реализацией,
// а настоящие вызовы "nft" — только в nftAccountant ниже.
//
// Отсутствие nftables в системе — штатный, а не аварийный случай: методы
// возвращают понятную ошибку, а вызывающий код (ClientManager) не считает
// её фатальной для заведения или удаления клиента — просто у клиента не
// будет цифр трафика, как и требует ТЗ-09 ("панель обязана работать и без
// счётчиков, просто без цифр").
type TrafficAccountant interface {
	// EnsureTables создаёт таблицу и обе цепочки, если их ещё нет.
	// Идемпотентна.
	EnsureTables() error
	// AddClient заводит по правилу-счётчику на клиента в каждой цепочке,
	// помеченному его id комментарием.
	AddClient(clientID string, uid int) error
	// RemoveClient убирает оба правила клиента, если они есть. Отсутствие
	// правил не ошибка.
	RemoveClient(clientID string) error
	// ReadCounters отдаёт текущие (не накопленные) значения счётчиков по
	// каждому клиенту, у кого они есть.
	ReadCounters() (map[string]RawCounter, error)
}

// nftAccountant — настоящая реализация поверх утилиты nft.
type nftAccountant struct{}

// NewNFTAccountant возвращает TrafficAccountant поверх настоящей nftables.
func NewNFTAccountant() TrafficAccountant { return nftAccountant{} }

func (nftAccountant) EnsureTables() error {
	return runNFTScript(setupScript())
}

func (nftAccountant) AddClient(clientID string, uid int) error {
	if !ValidUsername(clientID) {
		return fmt.Errorf("некорректный id клиента: %q", clientID)
	}
	if uid <= 0 {
		return fmt.Errorf("некорректный uid: %d", uid)
	}
	return runNFTScript(addClientScript(clientID, uid))
}

func (nftAccountant) RemoveClient(clientID string) error {
	if !ValidUsername(clientID) {
		return fmt.Errorf("некорректный id клиента: %q", clientID)
	}
	rules, err := listNFTRules()
	if err != nil {
		return err
	}
	var script strings.Builder
	found := false
	for _, r := range rules {
		if r.Comment != clientID {
			continue
		}
		found = true
		fmt.Fprintf(&script, "delete rule %s %s %s handle %d\n", nftFamily, nftTable, r.Chain, r.Handle)
	}
	if !found {
		return nil
	}
	return runNFTScript(script.String())
}

func (nftAccountant) ReadCounters() (map[string]RawCounter, error) {
	rules, err := listNFTRules()
	if err != nil {
		return nil, err
	}
	return countersByClient(rules), nil
}

// countersByClient сворачивает список правил (по одному на клиента в каждой
// из двух цепочек) в счётчики по id клиента — чистая функция, отдельно
// покрыта тестами вместе с ParseNFTRules.
func countersByClient(rules []NFTRule) map[string]RawCounter {
	out := map[string]RawCounter{}
	for _, r := range rules {
		if r.Comment == "" || r.Counter == nil {
			continue
		}
		c := out[r.Comment]
		switch r.Chain {
		case chainIn:
			c.RxBytes += r.Counter.Bytes
			c.RxPackets += r.Counter.Packets
		case chainOut:
			c.TxBytes += r.Counter.Bytes
			c.TxPackets += r.Counter.Packets
		}
		out[r.Comment] = c
	}
	return out
}

// AccumulateCounter добавляет к накопленному итогу разницу между новым и
// прошлым сырым значением счётчика nft. Если новое значение меньше
// прошлого, счётчик успел обнулиться (перезапуск nftables, пересоздание
// таблицы панелью) — в этом случае к итогу прибавляется целиком новое
// значение, а не отрицательная "разница": итог за это время не может
// уменьшиться, сколько бы раз nftables ни перезапускали.
func AccumulateCounter(prevRaw, prevTotal, newRaw uint64) (raw, total uint64) {
	if newRaw >= prevRaw {
		return newRaw, prevTotal + (newRaw - prevRaw)
	}
	return newRaw, prevTotal + newRaw
}

// setupScript — таблица и обе цепочки. "add table"/"add chain" в nftables
// сами по себе идемпотентны (повторный вызов на уже существующие не
// ошибка), поэтому отдельная проверка "уже есть или нет" не нужна.
func setupScript() string {
	return fmt.Sprintf(
		"add table %s %s\n"+
			"add chain %s %s %s { type filter hook input priority filter\\; policy accept\\; }\n"+
			"add chain %s %s %s { type filter hook output priority filter\\; policy accept\\; }\n",
		nftFamily, nftTable,
		nftFamily, nftTable, chainIn,
		nftFamily, nftTable, chainOut,
	)
}

// addClientScript — по правилу-счётчику на клиента в обеих цепочках. uid
// подставляется как число (проверено вызывающим кодом), clientID —
// провалидированное имя пользователя, безопасное для комментария nft: в нём
// нет ни кавычек, ни переводов строк (см. ValidUsername).
func addClientScript(clientID string, uid int) string {
	return fmt.Sprintf(
		"add rule %s %s %s meta skuid %d counter comment \"%s\"\n"+
			"add rule %s %s %s meta skuid %d counter comment \"%s\"\n",
		nftFamily, nftTable, chainIn, uid, clientID,
		nftFamily, nftTable, chainOut, uid, clientID,
	)
}

// runNFTScript передаёт текст правил в nft через stdin ("nft -f -"), а не
// собирает одну строку аргументов: так спецсимволы вроде ";" и "{}"
// экранируются один раз внутри самого скрипта, а не при сборке
// exec.Command.
func runNFTScript(script string) error {
	if _, err := exec.LookPath("nft"); err != nil {
		return errors.New("не нашёл nft в PATH — учёт трафика недоступен, панель работает без него")
	}
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("nft: %s", msg)
		}
		return fmt.Errorf("nft: %w", err)
	}
	return nil
}

func listNFTRules() ([]NFTRule, error) {
	if _, err := exec.LookPath("nft"); err != nil {
		return nil, errors.New("не нашёл nft в PATH — учёт трафика недоступен, панель работает без него")
	}
	out, err := exec.Command("nft", "-j", "list", "ruleset").Output()
	if err != nil {
		return nil, fmt.Errorf("nft -j list ruleset: %w", err)
	}
	all, err := ParseNFTRules(out)
	if err != nil {
		return nil, err
	}
	rules := make([]NFTRule, 0, len(all))
	for _, r := range all {
		if r.Chain == chainIn || r.Chain == chainOut {
			rules = append(rules, r)
		}
	}
	return rules, nil
}

// --- разбор "nft -j list ruleset" -----------------------------------------

type nftListDoc struct {
	Nftables []nftListItem `json:"nftables"`
}

type nftListItem struct {
	Rule *nftRuleJSON `json:"rule"`
}

type nftRuleJSON struct {
	Chain   string        `json:"chain"`
	Handle  int           `json:"handle"`
	Comment string        `json:"comment"`
	Expr    []nftExprJSON `json:"expr"`
}

// nftExprJSON — один элемент массива "expr" в правиле. Панель различает
// свои правила не разбором meta/match (это отдельная, более хрупкая
// структура в выводе nft), а комментарием на самом правиле (см. nftRuleJSON)
// — единственное, что здесь реально разбирается, это counter.
type nftExprJSON struct {
	Counter *nftCounterJSON `json:"counter"`
}

type nftCounterJSON struct {
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// ParseNFTRules разбирает JSON-вывод "nft -j list ruleset" (или "list
// table ...") в плоский список правил. Правила без comment или без counter
// в результат тоже попадают (Comment=="" или Counter==nil) — фильтрацию по
// нужным цепочкам и полям делает вызывающий код (listNFTRules,
// countersByClient), а не сам разбор.
func ParseNFTRules(data []byte) ([]NFTRule, error) {
	var doc nftListDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("не удалось разобрать вывод nft -j: %w", err)
	}
	var rules []NFTRule
	for _, item := range doc.Nftables {
		if item.Rule == nil {
			continue
		}
		r := NFTRule{Chain: item.Rule.Chain, Handle: item.Rule.Handle, Comment: item.Rule.Comment}
		for _, e := range item.Rule.Expr {
			if e.Counter != nil {
				r.Counter = &ruleCounter{Packets: e.Counter.Packets, Bytes: e.Counter.Bytes}
			}
		}
		rules = append(rules, r)
	}
	return rules, nil
}
