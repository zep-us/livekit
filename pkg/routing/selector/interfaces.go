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
	"errors"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"

	"github.com/livekit/livekit-server/pkg/config"
)

var ErrUnsupportedSelector = errors.New("unsupported node selector")

// PlacementContext carries optional, additive information about the room being placed.
// Selectors that don't need it ignore it; the spaceaffinity selector uses it to spread
// a space's rooms across nodes. Empty fields mean "no information" (graceful fallback).
type PlacementContext struct {
	// RoomName is the name of the room being placed.
	RoomName livekit.RoomName
	// SameSpaceLoadByNode maps a node id to the current LOAD that the same space already puts
	// on it — the sum of participant counts across that space's EXISTING rooms on the node, NOT
	// a raw room count (a single room can be very heavy). Derived on each placement from the live
	// room->node map + current room stats (no persistent counter), so stale entries from dead
	// nodes are naturally excluded and empty rooms contribute ~0.
	SameSpaceLoadByNode map[livekit.NodeID]float32
}

// NodeSelector selects an appropriate node to run the current session
type NodeSelector interface {
	SelectNode(nodes []*livekit.Node, ctx PlacementContext) (*livekit.Node, error)
}

func CreateNodeSelector(conf *config.Config) (NodeSelector, error) {
	kind := conf.NodeSelector.Kind
	if kind == "" {
		kind = "any"
	}
	switch kind {
	case "any":
		return &AnySelector{conf.NodeSelector.SortBy, conf.NodeSelector.Algorithm}, nil
	case "cpuload":
		return &CPULoadSelector{
			CPULoadLimit: conf.NodeSelector.CPULoadLimit,
			SortBy:       conf.NodeSelector.SortBy,
			Algorithm:    conf.NodeSelector.Algorithm,
		}, nil
	case "sysload":
		return &SystemLoadSelector{
			SysloadLimit: conf.NodeSelector.SysloadLimit,
			SortBy:       conf.NodeSelector.SortBy,
			Algorithm:    conf.NodeSelector.Algorithm,
		}, nil
	case "regionaware":
		s, err := NewRegionAwareSelector(conf.Region, conf.NodeSelector.Regions, conf.NodeSelector.SortBy, conf.NodeSelector.Algorithm)
		if err != nil {
			return nil, err
		}
		s.SysloadLimit = conf.NodeSelector.SysloadLimit
		return s, nil
	case "spaceaffinity":
		penalty := conf.NodeSelector.AffinityPenalty
		if penalty <= 0 {
			penalty = DefaultAffinityPenalty
		}
		return &SpaceAffinitySelector{
			SystemLoadSelector: SystemLoadSelector{
				SysloadLimit: conf.NodeSelector.SysloadLimit,
				SortBy:       conf.NodeSelector.SortBy,
				Algorithm:    conf.NodeSelector.Algorithm,
			},
			AffinityPenalty: penalty,
			SpaceRoomPrefix: conf.NodeSelector.SpaceRoomPrefix,
		}, nil
	case "random":
		logger.Warnw("random node selector is deprecated, please switch to \"any\" or another selector", nil)
		return &AnySelector{conf.NodeSelector.SortBy, conf.NodeSelector.Algorithm}, nil
	default:
		return nil, ErrUnsupportedSelector
	}
}
