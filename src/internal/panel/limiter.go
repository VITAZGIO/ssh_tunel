package panel

import (
	"sync"
	"time"
)

// loginLimiter тормозит перебор пароля: с каждой подряд неудачной попыткой
// растёт задержка перед ответом, а после нескольких подряд — учётная запись
// на время блокируется вовсе. Ключ — логин и адрес, с которого пришёл
// запрос: так подбор пароля к одной учётке с разных адресов и перебор чужих
// логинов с одного адреса тормозятся одинаково.
type loginLimiter struct {
	mu    sync.Mutex
	state map[string]*attemptState
}

type attemptState struct {
	fails       int
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{state: map[string]*attemptState{}}
}

// baseDelay и maxDelay задают, насколько нужно тормозить ответ при подряд
// неудачных попытках: 500мс, 1с, 2с... до потолка в 8с. Дольше — уже похоже
// на отказ в обслуживании самому себе, а перебор паролей такая задержка и
// так делает практически бесполезным.
const (
	baseDelay  = 500 * time.Millisecond
	maxDelay   = 8 * time.Second
	lockAfter = 8               // подряд неудач для блокировки
	lockFor   = 5 * time.Minute // на сколько блокируется учётка
)

// Before вызывается перед проверкой пароля. Если учётная запись сейчас
// заблокирована — возвращает оставшееся время блокировки и запрос дальше не
// идёт. Иначе задерживает выполнение на время, растущее с числом подряд
// неудачных попыток.
func (l *loginLimiter) Before(key string) (locked bool, remaining time.Duration) {
	l.mu.Lock()
	st, ok := l.state[key]
	if !ok {
		l.mu.Unlock()
		return false, 0
	}
	if !st.lockedUntil.IsZero() {
		if left := time.Until(st.lockedUntil); left > 0 {
			l.mu.Unlock()
			return true, left
		}
		// Блокировка истекла — считаем, что человек начинает заново.
		st.fails = 0
		st.lockedUntil = time.Time{}
	}
	delay := baseDelay << st.fails
	if delay > maxDelay || delay <= 0 {
		delay = maxDelay
	}
	l.mu.Unlock()
	if st.fails > 0 {
		time.Sleep(delay)
	}
	return false, 0
}

// Fail отмечает неудачную попытку и включает блокировку, если неудач подряд
// набралось слишком много.
func (l *loginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.state[key]
	if !ok {
		st = &attemptState{}
		l.state[key] = st
	}
	st.fails++
	if st.fails >= lockAfter {
		st.lockedUntil = time.Now().Add(lockFor)
	}
}

// Success сбрасывает счётчик — верный пароль подтверждает, что перед нами не
// перебор.
func (l *loginLimiter) Success(key string) {
	l.mu.Lock()
	delete(l.state, key)
	l.mu.Unlock()
}
