package routing

import "testing"

func TestModeAllSendsEverythingThroughTunnel(t *testing.T) {
	p := New(ModeAll, []string{"chrome.exe"})
	for _, name := range []string{"chrome.exe", "steam.exe", ""} {
		if !p.UseTunnel(name) {
			t.Fatalf("в режиме «всё через туннель» %q пошло мимо", name)
		}
	}
}

func TestModeOnly(t *testing.T) {
	p := New(ModeOnly, []string{"chrome.exe", "node.exe"})

	if !p.UseTunnel("chrome.exe") {
		t.Fatal("выбранная программа должна идти через туннель")
	}
	if !p.UseTunnel("node.exe") {
		t.Fatal("выбранная программа должна идти через туннель")
	}
	if p.UseTunnel("steam.exe") {
		t.Fatal("невыбранная программа не должна идти через туннель")
	}
}

func TestModeExcept(t *testing.T) {
	p := New(ModeExcept, []string{"steam.exe"})

	if p.UseTunnel("steam.exe") {
		t.Fatal("исключённая программа пошла через туннель")
	}
	if !p.UseTunnel("chrome.exe") {
		t.Fatal("не исключённая программа должна идти через туннель")
	}
}

// Имя пишут по-разному: с путём, в другом регистре, без расширения.
// Всё это должно считаться одной программой, иначе список не работает.
func TestNameMatchingIsForgiving(t *testing.T) {
	p := New(ModeExcept, []string{"Steam"})

	for _, name := range []string{
		"steam.exe",
		"STEAM.EXE",
		`C:\Program Files (x86)\Steam\steam.exe`,
		"steam",
	} {
		if p.UseTunnel(name) {
			t.Fatalf("%q не распознано как та же программа", name)
		}
	}
}

// Неизвестную программу ведём через туннель: неожиданная утечка мимо туннеля
// хуже, чем лишнее приложение внутри него.
func TestUnknownProcessGoesThroughTunnel(t *testing.T) {
	if !New(ModeOnly, []string{"chrome.exe"}).UseTunnel("") {
		t.Fatal("неопознанная программа выпущена мимо туннеля")
	}
	if !New(ModeExcept, []string{"chrome.exe"}).UseTunnel("") {
		t.Fatal("неопознанная программа выпущена мимо туннеля")
	}
}

// Режим «только выбранные» с пустым списком отрезал бы интернет вообще всем.
// Такой список считаем незаданным.
func TestEmptyListDoesNotCutEverythingOff(t *testing.T) {
	if !New(ModeOnly, nil).UseTunnel("chrome.exe") {
		t.Fatal("пустой список в режиме «только выбранные» отрезал весь трафик")
	}
}

func TestUnknownModeFallsBackToAll(t *testing.T) {
	p := New(Mode("что-то не то"), []string{"chrome.exe"})
	if p.Mode() != ModeAll {
		t.Fatalf("непонятный режим должен сводиться к «всё через туннель», а стал %q", p.Mode())
	}
}

// Правила меняются на ходу: останавливать туннель ради этого не нужно.
func TestRulesChangeLive(t *testing.T) {
	p := New(ModeAll, nil)
	if !p.UseTunnel("steam.exe") {
		t.Fatal("исходно всё должно идти через туннель")
	}
	p.Set(ModeExcept, []string{"steam.exe"})
	if p.UseTunnel("steam.exe") {
		t.Fatal("новое правило не применилось")
	}
	p.Set(ModeAll, nil)
	if !p.UseTunnel("steam.exe") {
		t.Fatal("возврат к режиму «всё через туннель» не сработал")
	}
}
