package core

// Блокировка рекламы и слежки на уровне DNS: список имён, на которые надо
// отвечать отказом, не выдавая ни настоящего, ни подставного адреса, — и
// список исключений поверх него, которые никогда не блокируются.
//
// Оба списка выключены по умолчанию и пусты, пока человек сам не загрузит
// список: никаких встроенных источников в коде нет.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// BlockList — списки блокировки и исключений. Проверка идёт не только по
// точному имени, но и по всем его родительским доменам: запись
// "ads.example.com" блокирует и "x.ads.example.com", а "example.com" в
// исключениях снимает блокировку со всех его поддоменов разом. Так делают и
// сами списки в формате hosts — они перечисляют поддомены явно, один на
// строку, а не шаблонами со звёздочкой.
type BlockList struct {
	mu    sync.RWMutex
	block map[string]struct{}
	allow map[string]struct{}
}

// NewBlockList строит список из уже разобранных имён (см. ParseBlockListText).
func NewBlockList(blocked, allowed []string) *BlockList {
	b := &BlockList{}
	b.Set(blocked, allowed)
	return b
}

// Set заменяет содержимое списков целиком.
func (b *BlockList) Set(blocked, allowed []string) {
	bm := make(map[string]struct{}, len(blocked))
	for _, n := range blocked {
		if n = normalizeBlockName(n); n != "" {
			bm[n] = struct{}{}
		}
	}
	am := make(map[string]struct{}, len(allowed))
	for _, n := range allowed {
		if n = normalizeBlockName(n); n != "" {
			am[n] = struct{}{}
		}
	}
	b.mu.Lock()
	b.block, b.allow = bm, am
	b.mu.Unlock()
}

// Empty — блокировать вообще нечего (список пуст или сам BlockList не задан).
func (b *BlockList) Empty() bool {
	if b == nil {
		return true
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.block) == 0
}

// Match говорит, надо ли заблокировать это имя. Исключения проверяются первыми
// и имеют приоритет над блокировкой на любом уровне вложенности.
func (b *BlockList) Match(name string) bool {
	if b == nil {
		return false
	}
	name = normalizeBlockName(name)
	if name == "" {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if walkUpDomain(b.allow, name) {
		return false
	}
	return walkUpDomain(b.block, name)
}

// walkUpDomain проверяет имя и по очереди его родительские домены:
// "a.b.example.com" → "b.example.com" → "example.com" → "com".
func walkUpDomain(set map[string]struct{}, name string) bool {
	for {
		if _, ok := set[name]; ok {
			return true
		}
		i := strings.IndexByte(name, '.')
		if i < 0 {
			return false
		}
		name = name[i+1:]
	}
}

func normalizeBlockName(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// ParseBlockListText разбирает список в формате hosts ("0.0.0.0 имя ...",
// "127.0.0.1 имя") и в формате "одно имя в строке" — они неотличимы построчно
// без анализа содержимого, поэтому разбираются одним проходом: если первое
// поле строки похоже на IP-адрес, именами считаются остальные поля, иначе —
// все поля строки. Комментарии после "#" и пустые строки пропускаются.
func ParseBlockListText(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if net.ParseIP(fields[0]) != nil {
			out = append(out, fields[1:]...)
			continue
		}
		out = append(out, fields...)
	}
	return out
}

// blockListHTTPClient — короткий срок нарочно: список может быть большим, а
// вешать обновление по кнопке на минуты незачем — лучше явно сказать, что не
// получилось, чем заставлять ждать.
var blockListHTTPClient = &http.Client{Timeout: 30 * time.Second}

// FetchBlockListSource берёт список по ссылке (http/https) или из локального
// файла (обычный путь или с префиксом "file://") и возвращает разобранные
// имена.
func FetchBlockListSource(source string) ([]string, error) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		resp, err := blockListHTTPClient.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("сервер ответил %s", resp.Status)
		}
		return ParseBlockListText(resp.Body), nil
	}

	path := strings.TrimPrefix(source, "file://")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseBlockListText(f), nil
}
