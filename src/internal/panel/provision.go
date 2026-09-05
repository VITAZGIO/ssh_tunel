package panel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Provisioner — всё, что панель делает с системными пользователями и их
// authorized_keys. Отдельный интерфейс ровно затем, чтобы логику заведения и
// удаления клиентов (client_manager.go) можно было проверить тестами с
// поддельной реализацией — настоящая (systemProvisioner) дальше в этом файле
// только вызывает useradd/usermod/userdel/pkill и не принимает никаких
// решений сама.
//
// Все методы получают уже проверенное ValidUsername имя — вызывающая
// сторона (client_manager.go) обязана проверить его перед вызовом, но
// systemProvisioner проверяет ещё раз сама: это системные вызовы, которым
// нельзя доверять чужой контроль на входе.
type Provisioner interface {
	// EnsureGroup создаёт группу sshtunnel, если её ещё нет. Идемпотентна.
	EnsureGroup() error
	// CreateUser заводит unix-пользователя username: без пароля (заблокирован
	// hash'ем '*', а не только заблокированной учёткой usermod -L — так его
	// не получится разблокировать штатной сменой пароля), с оболочкой
	// /usr/sbin/nologin, единственной группой sshtunnel, домашней папкой
	// /home/username. Кладёт открытый ключ в её ~/.ssh/authorized_keys.
	CreateUser(username, pubKeyLine string) error
	// DeleteUser убивает живые сессии пользователя и удаляет его вместе с
	// домашней папкой.
	DeleteUser(username string) error
	// KillSessions обрывает все процессы пользователя — используется и при
	// удалении, и при заморозке/отключении клиента (ТЗ-09).
	KillSessions(username string) error
	// WriteAuthorizedKeys перезаписывает файл authorized_keys пользователя
	// целиком (используется заморозкой/разморозкой из ТЗ-09: убрать или
	// вернуть его строку, не трогая остальное на диске).
	WriteAuthorizedKeys(username, content string) error
	// ReadAuthorizedKeys читает текущее содержимое файла. Отсутствующий файл
	// не ошибка — трактуется как пустой.
	ReadAuthorizedKeys(username string) (string, error)
	// UID отдаёт числовой uid пользователя — нужен, чтобы завести правило
	// учёта трафика (nft.go, ТЗ-09: правила стоят на "meta skuid <uid>") и
	// чтобы искать процессы клиента в /proc (online.go), не запрашивая его у
	// системы на каждый обход /proc.
	UID(username string) (int, error)
}

// systemProvisioner — настоящая реализация поверх useradd/usermod/userdel и
// файловой системы. Требует root: без него даже EnsureGroup не пройдёт.
type systemProvisioner struct{}

// NewSystemProvisioner возвращает Provisioner, который реально меняет
// систему. Используется в cmd/ssh_tunnel_panel; тесты используют свою
// поддельную реализацию.
func NewSystemProvisioner() Provisioner { return systemProvisioner{} }

func (systemProvisioner) EnsureGroup() error {
	if groupExists(sshdGroup) {
		return nil
	}
	return runSystem("groupadd", sshdGroup)
}

func groupExists(name string) bool {
	_, err := user.LookupGroup(name)
	return err == nil
}

func (systemProvisioner) CreateUser(username, pubKeyLine string) error {
	if !ValidUsername(username) {
		return fmt.Errorf("некорректное имя пользователя: %q", username)
	}
	home := homeDir(username)

	if err := runSystem("useradd",
		"--create-home", "--home-dir", home,
		"--shell", "/usr/sbin/nologin",
		"--gid", sshdGroup, "--no-user-group",
		username,
	); err != nil {
		return err
	}
	// Пароль убран полностью, а не заблокирован через usermod -L: '*' как
	// хеш не совпадёт ни с одним паролем, и в отличие от -L его нельзя
	// случайно "разблокировать" повторным вызовом passwd.
	if err := runSystem("usermod", "-p", "*", username); err != nil {
		return err
	}

	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("пользователь %s создан, но не находится сразу после: %w", username, err)
	}
	if err := writeAuthorizedKeysFile(u, pubKeyLine+"\n"); err != nil {
		return err
	}
	return nil
}

func (systemProvisioner) DeleteUser(username string) error {
	if !ValidUsername(username) {
		return fmt.Errorf("некорректное имя пользователя: %q", username)
	}
	// Сессии рвём до userdel: иначе живые процессы держат открытыми файлы в
	// домашней папке, и "userdel -r" на некоторых системах отказывается её
	// удалять или удаляет только частично.
	_ = killUserSessions(username)
	if err := runSystem("userdel", "--remove", username); err != nil {
		return err
	}
	return nil
}

func (systemProvisioner) KillSessions(username string) error {
	if !ValidUsername(username) {
		return fmt.Errorf("некорректное имя пользователя: %q", username)
	}
	return killUserSessions(username)
}

// killUserSessions использует pkill -KILL -u — процессов у пользователя без
// доступа к сессии и с ForceCommand nologin считаные единицы (сам сеанс
// sshd на каждое подключение), так что мгновенное завершение без обычного
// TERM->ожидание->KILL оправдано и не оставляет ничего непонятого.
func killUserSessions(username string) error {
	err := exec.Command("pkill", "-KILL", "-u", username).Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// pkill возвращает 1, если подходящих процессов не нашлось — это не
		// ошибка, а нормальный случай "клиент и так не был на связи".
		return nil
	}
	return fmt.Errorf("pkill -u %s: %w", username, err)
}

func (systemProvisioner) WriteAuthorizedKeys(username, content string) error {
	if !ValidUsername(username) {
		return fmt.Errorf("некорректное имя пользователя: %q", username)
	}
	u, err := user.Lookup(username)
	if err != nil {
		return err
	}
	return writeAuthorizedKeysFile(u, content)
}

func (systemProvisioner) ReadAuthorizedKeys(username string) (string, error) {
	if !ValidUsername(username) {
		return "", fmt.Errorf("некорректное имя пользователя: %q", username)
	}
	data, err := os.ReadFile(authorizedKeysPath(username))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (systemProvisioner) UID(username string) (int, error) {
	if !ValidUsername(username) {
		return 0, fmt.Errorf("некорректное имя пользователя: %q", username)
	}
	u, err := user.Lookup(username)
	if err != nil {
		return 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, fmt.Errorf("некорректный uid %q пользователя %s", u.Uid, username)
	}
	return uid, nil
}

func homeDir(username string) string { return filepath.Join("/home", username) }

func authorizedKeysPath(username string) string {
	return filepath.Join(homeDir(username), ".ssh", "authorized_keys")
}

// writeAuthorizedKeysFile создаёт ~/.ssh с правами 700 и authorized_keys с
// правами 600, оба — во владении самого клиента. sshd проверяет именно эти
// права (StrictModes) и молча отказывает в входе, если они более открытые.
func writeAuthorizedKeysFile(u *user.User, content string) error {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("некорректный uid %q пользователя %s", u.Uid, u.Username)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("некорректный gid %q пользователя %s", u.Gid, u.Username)
	}

	sshDir := filepath.Join(u.HomeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("не могу создать %s: %w", sshDir, err)
	}
	if err := os.Chown(sshDir, uid, gid); err != nil {
		return fmt.Errorf("не могу назначить владельца %s: %w", sshDir, err)
	}

	path := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("не могу записать %s: %w", path, err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("не могу назначить владельца %s: %w", path, err)
	}
	return nil
}

// runSystem выполняет системную команду и, если та завершилась с ошибкой,
// заворачивает в текст её собственный вывод — "useradd: user 'tun_xxx'
// already exists" куда понятнее голого "exit status 9". Отсутствие самой
// программы в PATH тоже отдаётся понятным текстом, а не куском Go-паники о
// ErrNotFound.
func runSystem(name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("не нашёл %s в PATH — команда нужна для управления клиентами панели", name)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s: %s", name, msg)
	}
	return fmt.Errorf("%s: %w", name, err)
}
