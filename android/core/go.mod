// Ядро версии для Android вынесено в отдельный модуль намеренно.
//
// Сетевой стек тянет за собой gvisor и требует Go 1.26, а настольной версии всё
// это не нужно: пусть она остаётся лёгкой, на Go 1.22 и трёх зависимостях.
// Путь модуля начинается с sshtunnel/, поэтому пакеты internal/ из основного
// модуля остаются доступными — правило внутренних пакетов смотрит на путь.
module sshtunnel/android/core

go 1.26.3

require (
	github.com/xjasonlyu/tun2socks/v2 v2.7.0
	golang.org/x/crypto v0.53.0
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.46.0
	gvisor.dev/gvisor v0.0.0-20260701204157-69c2d17aea96
	sshtunnel v0.0.0
)

require (
	github.com/go-task/slim-sprig v0.0.0-20230315185526-52ccab3ef572 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/pprof v0.0.0-20210407192527-94a9f03dee38 // indirect
	github.com/onsi/ginkgo/v2 v2.9.5 // indirect
	github.com/quic-go/quic-go v0.49.0 // indirect
	go.uber.org/mock v0.5.0 // indirect
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.46.0 // indirect
)

replace sshtunnel => ../../src
