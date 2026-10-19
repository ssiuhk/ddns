package shared

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRollover(t *testing.T) {
	store := &memoryKeyStore{}
	zone := "d.example.org"
	_, err := EnsureZoneIsSigned(store, zone)
	assert.Nil(t, err)
	before, _ := DSRecords(store, zone)

	step := func(expected RolloverStep) *RolloverPlan {
		t.Helper()
		plan, err := PlanRollover(store.keys)
		if assert.Nil(t, err) && assert.Equal(t, expected, plan.Step) {
			assert.Nil(t, ApplyRollover(store, zone, plan))
		}
		return plan
	}

	// both keys sign, and both DS records are valid
	started := step(RolloverStart)
	assert.Equal(t, 1, started.Old.ID)
	assert.Equal(t, 2, started.New.ID)
	ds, _ := DSRecords(store, zone)
	assert.Len(t, ds, 2)

	// the old key stops signing and is no longer offered as DS
	switched := step(RolloverSwitch)
	assert.Equal(t, 1, switched.Old.ID)
	assert.False(t, store.keys[0].Active)
	assert.True(t, store.keys[0].Published)
	ds, _ = DSRecords(store, zone)
	if assert.Len(t, ds, 1) {
		assert.NotEqual(t, before[0].Digest, ds[0].Digest)
	}

	finished := step(RolloverFinish)
	assert.Equal(t, 1, finished.Old.ID)
	if assert.Len(t, store.keys, 1) {
		assert.Equal(t, 2, store.keys[0].ID)
	}

	// and the next rollover starts over
	step(RolloverStart)

	_, err = PlanRollover(nil)
	assert.NotNil(t, err)
	_, err = PlanRollover([]*DomainKey{{ID: 1}, {ID: 2}})
	assert.NotNil(t, err)
}
