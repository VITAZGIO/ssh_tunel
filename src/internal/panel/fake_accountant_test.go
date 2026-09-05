package panel

import (
	"errors"
	"sync"
)

var errNFTUnavailable = errors.New("подставная ошибка: nft недоступен")

// fakeAccountant — TrafficAccountant в памяти для тестов client_manager.go,
// тем же принципом, что и fakeProvisioner: никакого настоящего nft, только
// заранее заданные показания счётчиков и учёт того, кого добавляли/удаляли.
type fakeAccountant struct {
	mu sync.Mutex

	tablesEnsured bool
	added         map[string]int // clientID -> uid
	removed       []string
	counters      map[string]RawCounter
	failRead      bool
}

func newFakeAccountant() *fakeAccountant {
	return &fakeAccountant{added: map[string]int{}, counters: map[string]RawCounter{}}
}

func (f *fakeAccountant) EnsureTables() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tablesEnsured = true
	return nil
}

func (f *fakeAccountant) AddClient(clientID string, uid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added[clientID] = uid
	return nil
}

func (f *fakeAccountant) RemoveClient(clientID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, clientID)
	delete(f.added, clientID)
	return nil
}

func (f *fakeAccountant) ReadCounters() (map[string]RawCounter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRead {
		return nil, errNFTUnavailable
	}
	out := make(map[string]RawCounter, len(f.counters))
	for k, v := range f.counters {
		out[k] = v
	}
	return out, nil
}

func (f *fakeAccountant) setCounter(clientID string, c RawCounter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[clientID] = c
}
