//go:build windows

// Package shutdown ловит все способы, которыми пользователь может закрыть
// программу, чтобы системный прокси успел вернуться на место.
//
// Одного signal.Notify мало: он ловит Ctrl+C, но НЕ ловит закрытие окна
// консоли крестиком, выход из системы и завершение сеанса. А это как раз
// самые обидные случаи — прокси остаётся включённым, и у человека полностью
// пропадает интернет, причём непонятно почему.
package shutdown

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleCtrlHandler = kernel32.NewProc("SetConsoleCtrlHandler")

	once sync.Once
	// handler держим в пакетной переменной: Windows вызовет его из своего
	// потока, и колбэк не должен быть собран сборщиком мусора.
	handlerRef uintptr
	onExit     func()
	done       = make(chan struct{})
)

const (
	ctrlCEvent        = 0
	ctrlBreakEvent    = 1
	ctrlCloseEvent    = 2 // окно закрыли крестиком
	ctrlLogoffEvent   = 5
	ctrlShutdownEvent = 6
)

// OnExit регистрирует функцию, которая выполнится при любом варианте выхода.
// Возвращает канал, закрывающийся после её выполнения.
func OnExit(fn func()) <-chan struct{} {
	once.Do(func() {
		onExit = fn

		cb := syscall.NewCallback(func(ctrlType uint32) uintptr {
			switch ctrlType {
			case ctrlCEvent, ctrlBreakEvent, ctrlCloseEvent, ctrlLogoffEvent, ctrlShutdownEvent:
				runExit()
				// На CTRL_CLOSE_EVENT система даёт около 5 секунд, после чего
				// убивает процесс принудительно. Нам нужно куда меньше.
				return 1
			}
			return 0
		})
		handlerRef = cb
		procSetConsoleCtrlHandler.Call(cb, 1)

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			runExit()
		}()
	})
	return done
}

var exitOnce sync.Once

func runExit() {
	exitOnce.Do(func() {
		if onExit != nil {
			onExit()
		}
		close(done)
	})
}

var _ = unsafe.Pointer(nil)
