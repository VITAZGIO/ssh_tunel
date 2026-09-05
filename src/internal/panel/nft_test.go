package panel

import (
	"strings"
	"testing"
)

// nftListFixture — вручную собранный фрагмент вывода "nft -j list ruleset"
// в формате, который строит addClientScript: по правилу с counter и
// comment=id клиента в каждой из двух цепочек панели, плюс одно чужое
// правило без комментария, которое разбор должен пропустить при сведении
// счётчиков (но не потерять при простом разборе).
const nftListFixture = `{
  "nftables": [
    {"metainfo": {"version": "1.0.9"}},
    {"table": {"family": "inet", "name": "ssh_tunnel_panel", "handle": 1}},
    {"chain": {"family": "inet", "table": "ssh_tunnel_panel", "name": "acct_in",
      "handle": 2, "type": "filter", "hook": "input", "prio": 0, "policy": "accept"}},
    {"chain": {"family": "inet", "table": "ssh_tunnel_panel", "name": "acct_out",
      "handle": 3, "type": "filter", "hook": "output", "prio": 0, "policy": "accept"}},
    {"rule": {"family": "inet", "table": "ssh_tunnel_panel", "chain": "acct_in", "handle": 4,
      "comment": "tun_0123456789abcdef",
      "expr": [
        {"match": {"op": "==", "left": {"meta": {"key": "skuid"}}, "right": 90001}},
        {"counter": {"packets": 120, "bytes": 45678}}
      ]}},
    {"rule": {"family": "inet", "table": "ssh_tunnel_panel", "chain": "acct_out", "handle": 5,
      "comment": "tun_0123456789abcdef",
      "expr": [
        {"match": {"op": "==", "left": {"meta": {"key": "skuid"}}, "right": 90001}},
        {"counter": {"packets": 200, "bytes": 98765}}
      ]}},
    {"rule": {"family": "inet", "table": "filter", "chain": "input", "handle": 6,
      "expr": [{"accept": null}]}}
  ]
}`

func TestParseNFTRules(t *testing.T) {
	rules, err := ParseNFTRules([]byte(nftListFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatalf("ожидал 3 правила, получил %d: %+v", len(rules), rules)
	}

	in := rules[0]
	if in.Chain != "acct_in" || in.Handle != 4 || in.Comment != "tun_0123456789abcdef" {
		t.Fatalf("неверно разобрано первое правило: %+v", in)
	}
	if in.Counter == nil || in.Counter.Bytes != 45678 || in.Counter.Packets != 120 {
		t.Fatalf("неверный счётчик первого правила: %+v", in.Counter)
	}

	other := rules[2]
	if other.Comment != "" || other.Counter != nil {
		t.Fatalf("постороннее правило без счётчика и комментария разобралось не пусто: %+v", other)
	}
}

func TestParseNFTRulesRejectsGarbage(t *testing.T) {
	if _, err := ParseNFTRules([]byte("not json at all")); err == nil {
		t.Fatal("мусор вместо JSON должен быть ошибкой")
	}
}

func TestCountersByClient(t *testing.T) {
	rules, err := ParseNFTRules([]byte(nftListFixture))
	if err != nil {
		t.Fatal(err)
	}
	counters := countersByClient(rules)
	if len(counters) != 1 {
		t.Fatalf("ожидал счётчики одного клиента, получил %d: %+v", len(counters), counters)
	}
	c := counters["tun_0123456789abcdef"]
	if c.RxBytes != 45678 || c.RxPackets != 120 {
		t.Fatalf("неверный rx: %+v", c)
	}
	if c.TxBytes != 98765 || c.TxPackets != 200 {
		t.Fatalf("неверный tx: %+v", c)
	}
}

func TestAccumulateCounterGrows(t *testing.T) {
	raw, total := AccumulateCounter(0, 0, 1000)
	if raw != 1000 || total != 1000 {
		t.Fatalf("первое чтение: получил raw=%d total=%d", raw, total)
	}
	raw, total = AccumulateCounter(raw, total, 1500)
	if raw != 1500 || total != 1500 {
		t.Fatalf("второе чтение (рост): получил raw=%d total=%d", raw, total)
	}
}

func TestAccumulateCounterSurvivesReset(t *testing.T) {
	// Счётчик nft обнулился (пересоздание таблицы, перезапуск nftables) —
	// новое сырое значение меньше прошлого, но накопленный итог не должен
	// уменьшиться, а должен продолжить расти на новое значение целиком.
	raw, total := AccumulateCounter(1500, 1500, 200)
	if raw != 200 {
		t.Fatalf("новое сырое значение должно сохраниться как есть, получил %d", raw)
	}
	if total != 1700 {
		t.Fatalf("итог должен вырасти на новое значение целиком (1500+200), получил %d", total)
	}
}

func TestSetupScriptAndAddClientScriptMentionBothChains(t *testing.T) {
	s := setupScript()
	for _, want := range []string{"add table inet ssh_tunnel_panel", "acct_in", "acct_out", "hook input", "hook output"} {
		if !strings.Contains(s, want) {
			t.Fatalf("setupScript() не содержит %q:\n%s", want, s)
		}
	}

	a := addClientScript("tun_0123456789abcdef", 90001)
	for _, want := range []string{"meta skuid 90001", "comment \"tun_0123456789abcdef\"", "acct_in", "acct_out"} {
		if !strings.Contains(a, want) {
			t.Fatalf("addClientScript() не содержит %q:\n%s", want, a)
		}
	}
}
