// Разбор типовых ошибок подключения по SSH — в одном месте, для всех
// потребителей ядра: консоли, локальной веб-панели и приложения на Android
// (через android/core/mobile, которое использует этот же пакет). Без этого
// человек видел сырой текст вида "ssh: handshake failed: ssh: unable to
// authenticate" — понять из него ничего нельзя, а причина почти всегда одна
// из трёх: ключ не подошёл (устройство заморозили или удалили в панели),
// сервер не отвечает, или порт закрыт.
package tunnel

import (
	"errors"
	"net"
	"strings"
	"syscall"
)

// ConnErrorKind — стабильный код причины отказа подключения. Экран каждой
// платформы переводит его в свой текст через собственный словарь I18N
// (внутри самого ядра текст только на русском — для консоли, у которой
// своего I18N нет).
type ConnErrorKind string

const (
	// ConnErrorAuth — сервер отверг ключ на этапе аутентификации SSH.
	ConnErrorAuth ConnErrorKind = "auth"
	// ConnErrorNoResponse — TCP-соединение не установилось за отведённое
	// время либо адрес не отвечает вовсе (таймаут, "no route to host",
	// "network is unreachable", имя не разрешилось).
	ConnErrorNoResponse ConnErrorKind = "no_response"
	// ConnErrorRefused — на порт пришёл явный отказ (RST): порт закрыт, или
	// там ничего не слушает.
	ConnErrorRefused ConnErrorKind = "refused"
	// ConnErrorOther — что-то ещё в самом обмене по SSH. Экран показывает
	// текст как есть, но с пометкой, что это ответ сервера, а не сбой самой
	// программы.
	ConnErrorOther ConnErrorKind = "other"
)

// ConnError — ошибка именно попытки открыть SSH-соединение (dial), уже
// классифицированная. Оборачивает только сбои самого обмена по сети/SSH —
// смена ключа сервера (hostkey.ErrChanged) под неё не подпадает и остаётся
// как есть, без изменений: это отдельная, самостоятельная защита, и её
// текст трогать не нужно (см. dial() в tunnel.go). Локальные ошибки вроде
// отсутствия файла ключа или занятого порта тоже в ConnError не заворачиваются
// — они не имеют отношения к тому, что ответил сервер.
type ConnError struct {
	Kind ConnErrorKind
	// Message — готовый русский текст: тот, что видит консоль, и запасной
	// вариант для экрана, если он не узнал Kind.
	Message string
	Err     error
}

func (e *ConnError) Error() string { return e.Message }
func (e *ConnError) Unwrap() error { return e.Err }

// classifyConnError разбирает ошибку dial() один раз для всех потребителей
// ядра и заворачивает её в ConnError с готовым русским текстом.
func classifyConnError(err error) *ConnError {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return &ConnError{Kind: ConnErrorAuth, Err: err, Message: "Сервер не принял ключ. " +
			"Устройство могли заморозить или удалить в панели, либо ключ не тот."}
	case isRefused(err):
		return &ConnError{Kind: ConnErrorRefused, Err: err,
			Message: "Порт закрыт или SSH на сервере не запущен."}
	case isNoResponse(err):
		return &ConnError{Kind: ConnErrorNoResponse, Err: err,
			Message: "Сервер не отвечает: проверь адрес и порт."}
	default:
		return &ConnError{Kind: ConnErrorOther, Err: err, Message: msg}
	}
}

// isRefused — соединение отклонено явным образом (RST на TCP SYN): порт
// закрыт, или сервис на нём не слушает.
func isRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return strings.Contains(err.Error(), "connection refused")
}

// isNoResponse — до адреса не достучаться вовсе: таймаут установки
// TCP-соединения, недоступная сеть или адрес не разрешился в IP.
func isNoResponse(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no such host")
}
