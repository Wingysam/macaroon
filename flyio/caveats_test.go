package flyio

import (
	"encoding/json"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/superfly/macaroon"
	"github.com/superfly/macaroon/resource_set"
)

func TestCaveatSerialization(t *testing.T) {
	cs := macaroon.NewCaveatSet(
		&Organization{ID: 123, Mask: macaroon.ActionRead},
		&Apps{Apps: resource_set.ResourceSet[uint64]{123: macaroon.ActionRead}},
		&FeatureSet{Features: resource_set.New(macaroon.ActionRead, "123")},
		&Volumes{Volumes: resource_set.New(macaroon.ActionRead, "123")},
		&Machines{Machines: resource_set.New(macaroon.ActionRead, "123")},
		&Mutations{Mutations: []string{"123"}},
		&ConfineUser{ID: 123},
		&ConfineOrganization{ID: 123},
		&IsUser{ID: 123},
		&MachineFeatureSet{Features: resource_set.New(macaroon.ActionRead, "123")},
		&FromMachine{ID: "asdf"},
		&Clusters{Clusters: resource_set.New(macaroon.ActionRead, "123")},
	)

	b, err := json.Marshal(cs)
	assert.NoError(t, err)

	cs2 := macaroon.NewCaveatSet()
	err = json.Unmarshal(b, cs2)
	assert.NoError(t, err)
	assert.Equal(t, cs, cs2)

	b, err = cs.MarshalMsgpack()
	assert.NoError(t, err)
	cs2, err = macaroon.DecodeCaveats(b)
	assert.NoError(t, err)
	assert.Equal(t, cs, cs2)
}
