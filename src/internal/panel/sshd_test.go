package panel

import (
	"strings"
	"testing"
)

func TestEnsureMatchBlockAddsOnce(t *testing.T) {
	original := "Port 22\nPasswordAuthentication no\n"
	updated, changed := ensureMatchBlock(original)
	if !changed {
		t.Fatal("на конфиге без блока changed должен быть true")
	}
	if updated == original {
		t.Fatal("текст должен измениться")
	}
	for _, want := range []string{
		sshdMarkerBegin,
		"Match Group " + sshdGroup,
		"PermitTTY no",
		"AllowAgentForwarding no",
		"PermitTunnel no",
		"X11Forwarding no",
		"AllowTcpForwarding yes",
		"ForceCommand /usr/sbin/nologin",
		sshdMarkerEnd,
	} {
		if !strings.Contains(updated, want) {
			t.Fatalf("в итоговом конфиге не нашёл %q:\n%s", want, updated)
		}
	}

	// Второй проход по уже обновлённому файлу ничего не меняет — блок
	// добавляется панелью один раз, а не при каждом старте заново.
	again, changed2 := ensureMatchBlock(updated)
	if changed2 {
		t.Fatal("повторный вызов на уже обновлённом конфиге не должен ничего менять")
	}
	if again != updated {
		t.Fatal("текст при отсутствии изменений должен вернуться как есть")
	}
}

func TestEnsureMatchBlockOnEmptyConfig(t *testing.T) {
	updated, changed := ensureMatchBlock("")
	if !changed {
		t.Fatal("на пустом конфиге changed должен быть true")
	}
	if !strings.Contains(updated, sshdMarkerBegin) {
		t.Fatalf("блок должен появиться даже в пустом файле: %q", updated)
	}
}

func TestEnsureMatchBlockPreservesExistingContent(t *testing.T) {
	original := "Port 2222"
	updated, changed := ensureMatchBlock(original)
	if !changed {
		t.Fatal("changed должен быть true")
	}
	if !strings.Contains(updated, "Port 2222") {
		t.Fatal("существующие строки конфига не должны пропасть")
	}
}
