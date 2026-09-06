package panel

import (
	"strings"
	"testing"
)

const testPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBhqRXOJ8mR7z1Kx1P4W9nQfQd0iM5o1oGZK1qk6zX9J"

func TestAuthorizedKeysLine(t *testing.T) {
	line, err := AuthorizedKeysLine(testPubKey, "tun_0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	want := "restrict,port-forwarding " + testPubKey + " tun_0123456789abcdef"
	if line != want {
		t.Fatalf("получил %q, хочу %q", line, want)
	}
}

func TestAuthorizedKeysLineRejectsBadInput(t *testing.T) {
	if _, err := AuthorizedKeysLine("", "tun_0123456789abcdef"); err == nil {
		t.Fatal("пустой ключ должен быть ошибкой")
	}
	if _, err := AuthorizedKeysLine(testPubKey+"\nrestrict,port-forwarding evil-line", "tun_0123456789abcdef"); err == nil {
		t.Fatal("перевод строки в ключе должен быть ошибкой — иначе одна запись станет двумя")
	}
	if _, err := AuthorizedKeysLine(testPubKey, "not a valid client id"); err == nil {
		t.Fatal("некорректный id клиента должен быть ошибкой")
	}
}

func TestParseAndRenderAuthorizedKeysRoundTrip(t *testing.T) {
	content := "# comment line\n" +
		"restrict,port-forwarding " + testPubKey + " tun_0123456789abcdef\n" +
		"\n" +
		"ssh-rsa AAAAB3NzaC1yc2E= someone@else\n"
	entries := ParseAuthorizedKeys(content)
	// Комментарий, ключ клиента, пустая строка, второй ключ, и финальная
	// пустая запись от завершающего "\n" в конце content — Split всегда даёт
	// её последним элементом, и именно она отвечает за то, что рендер потом
	// восстанавливает завершающий перевод строки.
	if len(entries) != 5 {
		t.Fatalf("ожидал 5 записей, получил %d: %+v", len(entries), entries)
	}
	if !entries[1].IsClient || entries[1].Comment != "tun_0123456789abcdef" {
		t.Fatalf("вторая запись должна быть распознана как клиентская: %+v", entries[1])
	}
	if entries[3].IsClient {
		t.Fatalf("чужой ключ без опций панели не должен считаться клиентским: %+v", entries[3])
	}

	rendered := RenderAuthorizedKeys(entries)
	if rendered != content {
		t.Fatalf("разбор+сборка не дали исходный текст:\nбыло:  %q\nстало: %q", content, rendered)
	}
}

func TestHasClientKeyAndRemoveClientKey(t *testing.T) {
	line, err := AuthorizedKeysLine(testPubKey, "tun_0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	content := line + "\n"

	if !HasClientKey(content, "tun_0123456789abcdef") {
		t.Fatal("ключ клиента должен находиться сразу после создания")
	}
	if HasClientKey(content, "tun_ffffffffffffffff") {
		t.Fatal("чужой id не должен находиться")
	}

	removed := RemoveClientKey(content, "tun_0123456789abcdef")
	if strings.Contains(removed, "tun_0123456789abcdef") {
		t.Fatalf("после удаления строки клиента в файле не должно остаться его id: %q", removed)
	}
	if HasClientKey(removed, "tun_0123456789abcdef") {
		t.Fatal("после RemoveClientKey HasClientKey должен вернуть false")
	}

	// Идемпотентность: повторное удаление уже отсутствующей записи не портит
	// остальное содержимое файла.
	again := RemoveClientKey(removed, "tun_0123456789abcdef")
	if again != removed {
		t.Fatalf("повторное удаление не должно менять файл: %q != %q", again, removed)
	}
}

func TestRemoveClientKeyKeepsOtherEntries(t *testing.T) {
	a, err := AuthorizedKeysLine(testPubKey, "tun_0000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	b, err := AuthorizedKeysLine(testPubKey, "tun_1111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	content := a + "\n" + b + "\n"

	removed := RemoveClientKey(content, "tun_0000000000000000")
	if !strings.Contains(removed, "tun_1111111111111111") {
		t.Fatalf("ключ другого клиента должен остаться: %q", removed)
	}
	if strings.Contains(removed, "tun_0000000000000000") {
		t.Fatalf("удалённый клиент не должен остаться: %q", removed)
	}
}
