package shared

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateHostOnlyOnceWhenConcurrent(t *testing.T) {
	r := testRedis(t)
	assert.Nil(t, r.Load(&Snapshot{Version: snapshotVersion}))

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = r.CreateHost(&Host{Hostname: "race", Ip: "10.0.0.1", Token: string(rune('a' + i))})
		}(i)
	}
	wg.Wait()

	created := 0
	for _, err := range errs {
		if err == nil {
			created++
		} else {
			assert.ErrorIs(t, err, ErrHostExists)
		}
	}
	assert.Equal(t, 1, created)
}
