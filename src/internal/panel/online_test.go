package panel

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// makeFakeProc создаёт /proc-подобную структуру: по каталогу на pid с
// файлами status (строка "Uid:\t<uid>\t...") и comm (имя процесса).
func makeFakeProc(t *testing.T, procs map[int]struct {
	uid  int
	comm string
}) string {
	t.Helper()
	root := t.TempDir()
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		status := "Name:\t" + p.comm + "\n" +
			"Uid:\t" + strconv.Itoa(p.uid) + "\t" + strconv.Itoa(p.uid) + "\t" + strconv.Itoa(p.uid) + "\t" + strconv.Itoa(p.uid) + "\n"
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Не-числовой каталог вроде /proc/self — обход должен его пропустить, а
	// не упасть на нём.
	if err := os.MkdirAll(filepath.Join(root, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestCountUserSessionsCountsOnlySSHDForGivenUID(t *testing.T) {
	root := makeFakeProc(t, map[int]struct {
		uid  int
		comm string
	}{
		100: {uid: 90001, comm: "sshd"},
		101: {uid: 90001, comm: "sshd"},
		102: {uid: 90001, comm: "bash"}, // тот же uid, но не sshd — не считается
		103: {uid: 90002, comm: "sshd"}, // sshd, но другой uid — не считается
	})

	n, err := CountUserSessions(root, 90001)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ожидал 2 сессии, получил %d", n)
	}

	n, err = CountUserSessions(root, 90002)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ожидал 1 сессию, получил %d", n)
	}

	n, err = CountUserSessions(root, 90099)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ожидал 0 сессий для незнакомого uid, получил %d", n)
	}
}

func TestCountUserSessionsMissingRoot(t *testing.T) {
	if _, err := CountUserSessions(filepath.Join(t.TempDir(), "does-not-exist"), 1); err == nil {
		t.Fatal("несуществующий корень должен быть ошибкой")
	}
}
