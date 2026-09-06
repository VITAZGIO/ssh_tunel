package panel

import (
	"testing"
	"time"
)

func TestLoginLimiterDelaysAndLocks(t *testing.T) {
	l := newLoginLimiter()
	key := "admin|1.2.3.4"

	// Первая попытка — без задержки.
	if locked, _ := l.Before(key); locked {
		t.Fatal("свежий ключ не должен быть заблокирован")
	}
	l.Fail(key)

	start := time.Now()
	if locked, _ := l.Before(key); locked {
		t.Fatal("после одной неудачи блокировки быть не должно")
	}
	if elapsed := time.Since(start); elapsed < baseDelay {
		t.Fatalf("вторая попытка должна тормозиться минимум на %v, прошло %v", baseDelay, elapsed)
	}

	for i := 0; i < lockAfter; i++ {
		l.Fail(key)
	}
	locked, remaining := l.Before(key)
	if !locked {
		t.Fatal("после серии неудач учётка должна быть заблокирована")
	}
	if remaining <= 0 {
		t.Fatal("оставшееся время блокировки должно быть положительным")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	l := newLoginLimiter()
	key := "admin|1.2.3.4"
	l.Fail(key)
	l.Success(key)
	start := time.Now()
	if locked, _ := l.Before(key); locked {
		t.Fatal("успешный вход должен снимать блокировку")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("после успеха задержки быть не должно, прошло %v", elapsed)
	}
}
