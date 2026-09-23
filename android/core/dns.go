package core

// Ответы на DNS-запросы с телефона.
//
// Запросы приходят по UDP, а нести их через SSH нечем. Вместо пересылки мы
// отвечаем сами: имени выдаётся подставной адрес, а настоящее разрешение
// произойдёт на сервере в момент соединения. Наружу с телефона не уходит
// ни одного запроса — и в сети не видно, какие сайты открываются.
//
// Исключение — имена, которым положено идти мимо туннеля: домашний NAS,
// рабочая сеть. Их надо разрешать по-настоящему и локально, для чего нужен
// резолвер самой системы (на Android его предоставляет Kotlin-часть, потому
// что файла /etc/resolv.conf там нет).

import (
	"errors"
	"net"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// LocalResolve разрешает имя средствами самой системы, минуя туннель.
type LocalResolve func(name string) ([]net.IP, error)

// DNS отвечает на запросы приложений.
type DNS struct {
	// Pool раздаёт подставные адреса.
	Pool *FakePool

	// Direct говорит, что имя должно идти мимо туннеля. Обычно сюда
	// подставляются те же правила, что и в настольной версии.
	Direct func(name string) bool

	// Local разрешает такие имена по-настоящему. Если не задан, на них
	// возвращается отказ: выдать подставной адрес было бы хуже — соединение
	// ушло бы в туннель вместо домашней сети.
	Local LocalResolve

	// Block — список рекламы и слежки. В отличие от Direct (имя просто идёт
	// мимо туннеля), заблокированное имя не получает вообще никакого адреса:
	// ни настоящего, ни подставного. nil или пустой список — блокировка
	// выключена, ничего не меняется.
	Block *BlockList

	// Stats считает заблокированные запросы, если задан. Тот же счётчик, что
	// и для остального DNS/UDP-трафика — см. stack.go.
	Stats *Stats

	// TTL ответа в секундах. Держим маленьким: подставные адреса
	// переиспользуются, и залежавшийся ответ в кеше приложения будет мешать.
	TTL uint32
}

const defaultTTL = 10

// Answer разбирает запрос и готовит ответ. Возвращает пакет для отправки
// обратно приложению.
func (d *DNS) Answer(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}

	name := strings.TrimSuffix(q.Name.String(), ".")
	ttl := d.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}

	// Заблокированное имя отсекаем раньше всего остального: раньше Direct,
	// раньше выдачи подставного адреса, раньше запроса к Local. Иначе оно
	// заняло бы слот в пуле подставных адресов или ушло бы резолвером
	// системы — то есть слежка бы состоялась, просто мимо туннеля.
	if d.Block != nil && d.Block.Match(name) {
		if d.Stats != nil {
			d.Stats.block()
		}
		return refuse(h, q, dnsmessage.RCodeNameError)
	}

	reply := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 h.ID,
		Response:           true,
		OpCode:             h.OpCode,
		RecursionDesired:   h.RecursionDesired,
		RecursionAvailable: true,
	})
	reply.EnableCompression()
	if err := reply.StartQuestions(); err != nil {
		return nil, err
	}
	if err := reply.Question(q); err != nil {
		return nil, err
	}
	if err := reply.StartAnswers(); err != nil {
		return nil, err
	}

	// Отвечаем записями только на запрос адреса IPv4. На всё остальное —
	// пустой, но успешный ответ.
	//
	// Это касается и AAAA, и записи HTTPS, которую спрашивают браузеры: в ней
	// сайт сообщает, что умеет HTTP/3. Ответить на неё значит подтолкнуть
	// браузер к QUIC, то есть к UDP, который через SSH всё равно не пройдёт.
	if q.Type != dnsmessage.TypeA || q.Class != dnsmessage.ClassINET {
		return reply.Finish()
	}

	addrs, err := d.resolve(name)
	if err != nil {
		return refuse(h, q, dnsmessage.RCodeServerFailure)
	}
	for _, ip := range addrs {
		v4 := ip.To4()
		if v4 == nil {
			continue
		}
		var a [4]byte
		copy(a[:], v4)
		err := reply.AResource(dnsmessage.ResourceHeader{
			Name:  q.Name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
			TTL:   ttl,
		}, dnsmessage.AResource{A: a})
		if err != nil {
			return nil, err
		}
	}
	return reply.Finish()
}

// resolve решает, каким адресом ответить: подставным или настоящим.
func (d *DNS) resolve(name string) ([]net.IP, error) {
	if d.Direct != nil && d.Direct(name) {
		if d.Local == nil {
			return nil, errors.New("нет резолвера для имён, идущих мимо туннеля")
		}
		return d.Local(name)
	}
	if d.Pool == nil {
		return nil, errors.New("не задан пул подставных адресов")
	}
	return []net.IP{d.Pool.Get(name)}, nil
}

func refuse(h dnsmessage.Header, q dnsmessage.Question, code dnsmessage.RCode) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 h.ID,
		Response:           true,
		OpCode:             h.OpCode,
		RecursionDesired:   h.RecursionDesired,
		RecursionAvailable: true,
		RCode:              code,
	})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	return b.Finish()
}
