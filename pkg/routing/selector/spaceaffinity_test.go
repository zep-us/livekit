// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package selector_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/livekit-server/pkg/routing/selector"
)

const testSpacePrefix = "live_Space_"

func affNode(id string, loadAvg float32) *livekit.Node {
	return &livekit.Node{
		Id:    id,
		State: livekit.NodeState_SERVING,
		Stats: &livekit.NodeStats{
			UpdatedAt:       time.Now().Unix(),
			NumCpus:         1,
			LoadAvgLast1Min: loadAvg,
		},
	}
}

func TestSpaceKeyOf(t *testing.T) {
	cases := []struct {
		room   string
		prefix string
		want   string
	}{
		{"live_Space_ev1_m9", testSpacePrefix, "live_Space_ev1"},
		{"live_Space_ev1", testSpacePrefix, "live_Space_ev1"}, // no map segment
		{"live_Space_ab3_xy9_extra", testSpacePrefix, "live_Space_ab3"},
		{"prod_Space_ev1_m9", testSpacePrefix, ""}, // prefix mismatch
		{"live_Space_", testSpacePrefix, ""},       // empty space id
		{"live_Space_ev1_m9", "", ""},              // empty prefix -> disabled
	}
	for _, c := range cases {
		require.Equal(t, c.want, selector.SpaceKeyOf(livekit.RoomName(c.room), c.prefix), "room=%s prefix=%s", c.room, c.prefix)
	}
}

func TestSameSpaceRoomsOnLiveNodes(t *testing.T) {
	target := livekit.RoomName("live_Space_ev1_m1")
	assignments := map[livekit.RoomName]livekit.NodeID{
		"live_Space_ev1_m2": "nA",    // same space -> nA
		"live_Space_ev1_m3": "nA",    // same space -> nA
		"live_Space_ev2_m1": "nB",    // different space -> excluded
		"live_Space_ev1_m4": "nDEAD", // same space but dead node -> excluded
		"live_Space_ev1_m1": "nA",    // the target room itself -> excluded
	}
	live := []*livekit.Node{affNode("nA", 0.1), affNode("nB", 0.1)}

	rooms := selector.SameSpaceRoomsOnLiveNodes(target, assignments, live, testSpacePrefix)
	require.Len(t, rooms, 2)
	require.Equal(t, livekit.NodeID("nA"), rooms["live_Space_ev1_m2"])
	require.Equal(t, livekit.NodeID("nA"), rooms["live_Space_ev1_m3"])
	_, hasDifferentSpace := rooms["live_Space_ev2_m1"]
	require.False(t, hasDifferentSpace)
	_, hasDead := rooms["live_Space_ev1_m4"]
	require.False(t, hasDead) // dead node excluded (self-healing)

	// no resolvable space -> nil
	require.Nil(t, selector.SameSpaceRoomsOnLiveNodes(livekit.RoomName("foo"), assignments, live, testSpacePrefix))
}

func newSpaceAffinitySelector() *selector.SpaceAffinitySelector {
	return &selector.SpaceAffinitySelector{
		SystemLoadSelector: selector.SystemLoadSelector{
			SysloadLimit: 0.9,
			SortBy:       "sysload",
			Algorithm:    "lowest",
		},
		AffinityPenalty: 0.01, // load-equivalent per same-space participant
		SpaceRoomPrefix: testSpacePrefix,
	}
}

func TestSpaceAffinitySelector_GracefulDegradation(t *testing.T) {
	sel := newSpaceAffinitySelector()
	nLow, nHigh := affNode("nLow", 0.1), affNode("nHigh", 0.5)

	// every candidate carries the same same-space load -> choice degenerates to lowest base load
	pc := selector.PlacementContext{
		RoomName:            "live_Space_ev1_m9",
		SameSpaceLoadByNode: map[livekit.NodeID]float32{"nLow": 50, "nHigh": 50},
	}
	node, err := sel.SelectNode([]*livekit.Node{nHigh, nLow}, pc)
	require.NoError(t, err)
	require.Equal(t, nLow, node)
}

func TestSpaceAffinitySelector_SteersAwayFromHeavySpaceRoom(t *testing.T) {
	sel := newSpaceAffinitySelector()
	// similar base load, but n1 already carries a single HEAVY room of this space (200 participants)
	// -> prefer the empty n2 even though it is "the same room count" elsewhere.
	n1, n2 := affNode("n1", 0.3), affNode("n2", 0.3)
	pc := selector.PlacementContext{
		RoomName:            "live_Space_ev1_m9",
		SameSpaceLoadByNode: map[livekit.NodeID]float32{"n1": 200},
	}
	node, err := sel.SelectNode([]*livekit.Node{n1, n2}, pc)
	require.NoError(t, err)
	require.Equal(t, n2, node)
}

func TestSpaceAffinitySelector_LoadGateWins(t *testing.T) {
	sel := newSpaceAffinitySelector()
	// overloaded node carries zero same-space load, but must NOT be selected (hard load gate)
	overloaded := affNode("nOver", 0.95) // > SysloadLimit 0.9
	ok := affNode("nOk", 0.5)
	pc := selector.PlacementContext{
		RoomName:            "live_Space_ev1_m9",
		SameSpaceLoadByNode: map[livekit.NodeID]float32{"nOk": 300},
	}
	node, err := sel.SelectNode([]*livekit.Node{overloaded, ok}, pc)
	require.NoError(t, err)
	require.Equal(t, ok, node, "must not pick an overloaded node even with lower affinity")
}

func TestSpaceAffinitySelector_FallbackWithoutSpace(t *testing.T) {
	sel := newSpaceAffinitySelector()
	nLow, nHigh := affNode("nLow", 0.1), affNode("nHigh", 0.6)

	// room name does not match the prefix -> no space info -> behaves like system-load selector
	node, err := sel.SelectNode([]*livekit.Node{nHigh, nLow}, selector.PlacementContext{RoomName: "no_space_room"})
	require.NoError(t, err)
	require.Equal(t, nLow, node)
}
