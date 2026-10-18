package shared

import (
	"errors"
	"fmt"
)

// A key rollover replaces the signing key of a zone without breaking
// validation, using double signatures, which suits a combined signing key:
//
//  1. Start: a new key is added and signs alongside the old one. Once the
//     new DNSKEY has propagated, its DS is added at the parent, by hand or
//     from the CDS records.
//  2. Switch: once resolvers know the new DS, the old key stops signing but
//     stays published, for signatures still cached. The old DS may stay, as
//     one DS that matches a signing key is enough.
//  3. Finish: once those have expired, the old key is removed, after which
//     the old DS can go.
//
// Every run of the rollover command does the next step, decided from the
// state of the keys.
type RolloverStep int

const (
	RolloverStart RolloverStep = iota + 1
	RolloverSwitch
	RolloverFinish
)

// RolloverPlan is the next step of a rollover and the keys involved. New is
// only known after the start step has created it.
type RolloverPlan struct {
	Step RolloverStep
	Old  *DomainKey
	New  *DomainKey
}

// PlanRollover decides the next rollover step from the keys of a zone,
// sorted by ID as GetDomainKeys returns them.
func PlanRollover(keys []*DomainKey) (*RolloverPlan, error) {
	var active, inactive []*DomainKey
	for _, key := range keys {
		if key.Active {
			active = append(active, key)
		} else {
			inactive = append(inactive, key)
		}
	}

	switch {
	case len(keys) == 0:
		return nil, errors.New("the zone has no DNSSEC key")
	case len(active) == 1 && len(inactive) == 0:
		return &RolloverPlan{Step: RolloverStart, Old: active[0]}, nil
	case len(active) == 2 && len(inactive) == 0:
		return &RolloverPlan{Step: RolloverSwitch, Old: active[0], New: active[1]}, nil
	case len(active) == 1 && len(inactive) == 1 && inactive[0].ID < active[0].ID && inactive[0].Published:
		return &RolloverPlan{Step: RolloverFinish, Old: inactive[0], New: active[0]}, nil
	}
	return nil, fmt.Errorf("the keys are in a state the rollover does not know (%d active, %d inactive), check them with `ddns dnssec status`",
		len(active), len(inactive))
}

// ApplyRollover carries out a planned step. For the start step it creates
// the new key and sets plan.New.
func ApplyRollover(store KeyStore, zone string, plan *RolloverPlan) error {
	switch plan.Step {
	case RolloverStart:
		key, err := GenerateCSK(zone)
		if err != nil {
			return err
		}
		id, err := store.AddDomainKey(zone, key)
		if err != nil {
			return err
		}
		key.ID = id
		plan.New = key
		return nil
	case RolloverSwitch:
		return store.UpdateDomainKey(zone, plan.Old.ID, func(key *DomainKey) {
			key.Active = false
			key.Published = true
		})
	case RolloverFinish:
		return store.RemoveDomainKey(zone, plan.Old.ID)
	}
	return fmt.Errorf("unknown rollover step %d", plan.Step)
}
