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

package selector

import (
	"strings"

	"github.com/livekit/protocol/livekit"
)

// DefaultAffinityPenalty is the load-equivalent added per same-space PARTICIPANT already on a
// node, used when the config does not set affinity_penalty. The default 0.01 means ~100 same-space
// participants on a node count as +1.0 of system load when choosing where a NEW room starts — so a
// single heavy room (e.g. an event with hundreds of participants) strongly steers new rooms away,
// while a handful of small rooms barely matters.
const DefaultAffinityPenalty float32 = 0.01

// SpaceAffinitySelector spreads the rooms that belong to one "space" across nodes by the LOAD they
// actually put on each node — NOT by raw room count, because a single room of a space can be very
// heavy (many publishers/participants) while another space has many tiny rooms.
//
// It first applies the system-load gate (so overloaded nodes are never selected), then, among the
// remaining nodes, picks the one with the lowest combined score:
//
//	score(node) = sysload(node) + affinityPenalty * sameSpaceLoad(node)
//
// where sameSpaceLoad(node) is the sum of participant counts of the same space's EXISTING rooms on
// that node (see PlacementContext.SameSpaceLoadByNode). Properties:
//   - A single heavy room dominates the penalty (its participant count), so it is treated as heavy
//     even though it is "one room".
//   - When every candidate carries the same same-space load (e.g. all zero), the penalty term is
//     uniform and the choice degenerates to pure lowest-load.
//   - The load is DERIVED on each placement from the live room->node map + current room stats (no
//     persistent counter), so a crashed node's stale rooms self-heal and empty rooms add ~0.
//
// Stickiness is unaffected: an existing room keeps its node (the allocator returns early before any
// selector runs), so this only governs where a NEW room starts.
type SpaceAffinitySelector struct {
	SystemLoadSelector

	// AffinityPenalty is the load-equivalent penalty added per same-space participant already on a node.
	AffinityPenalty float32
	// SpaceRoomPrefix is the room-name prefix used to recognize/parse the space identity.
	// When empty, the selector has no space information and behaves like SystemLoadSelector.
	SpaceRoomPrefix string
}

func (s *SpaceAffinitySelector) SelectNode(nodes []*livekit.Node, ctx PlacementContext) (*livekit.Node, error) {
	// hard gate first: never pick an overloaded/unavailable node regardless of affinity
	nodes, err := s.SystemLoadSelector.filterNodes(nodes)
	if err != nil {
		return nil, err
	}

	spaceKey := SpaceKeyOf(ctx.RoomName, s.SpaceRoomPrefix)
	load := ctx.SameSpaceLoadByNode
	if spaceKey == "" || len(load) == 0 {
		// no resolvable space / no occupancy info -> behave like the system-load selector
		return SelectSortedNode(nodes, s.SortBy, s.Algorithm)
	}

	penalty := s.AffinityPenalty
	if penalty <= 0 {
		penalty = DefaultAffinityPenalty
	}

	var best *livekit.Node
	var bestScore float32
	for _, node := range nodes {
		var sysload float32
		if node.Stats != nil {
			sysload = GetNodeSysload(node)
		}
		score := sysload + penalty*load[livekit.NodeID(node.Id)]
		if best == nil || score < bestScore {
			best = node
			bestScore = score
		}
	}

	if best == nil {
		return nil, ErrNoAvailableNodes
	}
	return best, nil
}

// SpaceKeyOf extracts the space identity from a room name given a prefix.
//
// ZEP encodes the space in the room name as "<prefix><spaceId>_<mapId>"
// (e.g. prefix "live_Space_" -> "live_Space_ab3_xy9" has space key "live_Space_ab3").
// It anchors on the literal prefix (not a naive '_' split, since the prefix itself
// contains underscores) and takes the first segment after it as the space id.
//
// Returns "" when the prefix is empty or the room name does not match, in which case the
// selector falls back to load-only placement.
func SpaceKeyOf(roomName livekit.RoomName, prefix string) string {
	if prefix == "" {
		return ""
	}
	name := string(roomName)
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := name[len(prefix):]
	if idx := strings.IndexByte(rest, '_'); idx >= 0 {
		rest = rest[:idx]
	}
	if rest == "" {
		return ""
	}
	return prefix + rest
}

// SameSpaceRoomsOnLiveNodes returns the EXISTING rooms that belong to the same space as targetRoom,
// mapped to the node hosting them. Only live nodes are considered and the target room itself is
// excluded, so a crashed node's stale room->node entries (which the redis router never cleans up)
// are dropped. The caller then loads each room's current stats to weight the per-node load.
// Returns nil when targetRoom has no resolvable space.
func SameSpaceRoomsOnLiveNodes(
	targetRoom livekit.RoomName,
	assignments map[livekit.RoomName]livekit.NodeID,
	liveNodes []*livekit.Node,
	prefix string,
) map[livekit.RoomName]livekit.NodeID {
	targetSpace := SpaceKeyOf(targetRoom, prefix)
	if targetSpace == "" {
		return nil
	}

	live := make(map[livekit.NodeID]struct{}, len(liveNodes))
	for _, node := range liveNodes {
		live[livekit.NodeID(node.Id)] = struct{}{}
	}

	out := make(map[livekit.RoomName]livekit.NodeID)
	for room, nodeID := range assignments {
		if room == targetRoom {
			continue
		}
		if _, ok := live[nodeID]; !ok {
			continue // stale assignment on a dead node -> excluded (self-healing)
		}
		if SpaceKeyOf(room, prefix) == targetSpace {
			out[room] = nodeID
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
