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
	golang.org/x/sys v0.46.0
	gvisor.dev/gvisor v0.0.0-20260701204157-69c2d17aea96
	sshtunnel v0.0.0
)

require (
	github.com/google/btree v1.1.3 // indirect
	golang.org/x/exp v0.0.0-20260611194520-c48552f49976 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace sshtunnel => ../../src
