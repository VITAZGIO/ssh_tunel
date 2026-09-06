package tunnel

// Классификация ошибок SSH-подключения — общая для самопроверки, для
// автовыбора сервера (не переключаться на запасной, если дело в ключе или
// имени пользователя: другой сервер это не починит) и для мастера настройки
// VPS. golang.org/x/crypto/ssh не заводит для этого отдельный тип ошибки,
// только текст, поэтому строковая проверка здесь — не небрежность, а
// единственный способ.

import (
	"errors"
	"strings"
)

// IsAuthError — ошибка из-за неверного ключа или имени пользователя, а не
// из-за сети. Отличать важно: смена сервера чинит сетевые проблемы, но не
// эту — человек просто окажется там же с тем же ключом.
//
// Сначала спрашиваем классифицированную ошибку (*ConnError, connerror.go), и
// только потом смотрим на текст. Порядок именно такой не для красоты: у
// ConnError метод Error() возвращает готовое русское сообщение, английского
// "unable to authenticate" в нём уже нет — проверка по одному лишь тексту
// молча считала бы отказ по ключу сетевым сбоем и уводила бы на запасной
// сервер, где ровно тот же ключ и ровно тот же отказ.
func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	var ce *ConnError
	if errors.As(err, &ce) {
		return ce.Kind == ConnErrorAuth
	}
	return strings.Contains(err.Error(), "unable to authenticate")
}

// classifyDialCode — машиночитаемая причина сетевой неудачи, для шагов
// самопроверки и подобных мест, где текст на экране собирает сам интерфейс,
// а не Go.
func classifyDialCode(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case IsAuthError(err):
		return "auth_failed"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return "timeout"
	case strings.Contains(msg, "connection refused"):
		return "refused"
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return "unreachable"
	default:
		return "error"
	}
}
