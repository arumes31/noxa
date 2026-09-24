// control.go implements the Control service (232) on top of the ServerQuery
// backend, so gRPC and the query port cannot drift apart.
package grpcserver

import (
	"context"
	"strconv"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/query"
	"noxa/internal/safecast"
	noxav1 "noxa/v1"
)

const maxInvalidChannelWarnings = 8

// controlService serves administration RPCs. The file-transfer RPCs are
// deliberately left to UnimplementedControlServer (codes.Unimplemented): the
// transfer tokens they would issue are minted by the control channel after a
// per-client permission check, and a bot API that hands out its own tokens
// would be a second, unchecked path to the file port.
type controlService struct {
	noxav1.UnimplementedControlServer
	backend query.Backend
	logger  *zap.Logger
}

// ListChannels returns the channel tree, optionally rooted at one channel.
func (c *controlService) ListChannels(ctx context.Context, req *noxav1.ListChannelsRequest) (*noxav1.ListChannelsResponse, error) {
	var root int64
	if v := req.GetRootChannelId(); v != "" {
		parsed, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "root_channel_id must be numeric")
		}
		root = parsed
	}
	if root < 0 {
		return nil, status.Error(codes.InvalidArgument, "root_channel_id must be nonnegative")
	}
	return c.listRoleChannels(ctx, root)
}

func (c *controlService) channelListResponse(channels []query.ChannelInfo, root int64) *noxav1.ListChannelsResponse {
	all := uniqueChannels(channels)
	keep := subtreeCanonical(all, root)
	resp := &noxav1.ListChannelsResponse{}
	warnings := 0
	for _, ch := range all {
		if !keep[ch.ChannelID] {
			continue
		}
		maxClients, err := safecast.IntToInt32(ch.MaxClients)
		if err != nil || ch.MaxClients < 0 {
			c.warnInvalidChannelRow(ch.ChannelID, "max_clients", &warnings)
			continue
		}
		currentClients, err := safecast.IntToInt32(ch.ClientCount)
		if err != nil || ch.ClientCount < 0 {
			c.warnInvalidChannelRow(ch.ChannelID, "current_clients", &warnings)
			continue
		}
		resp.Channels = append(resp.Channels, &noxav1.Channel{
			Id:             strconv.FormatInt(ch.ChannelID, 10),
			Name:           ch.Name,
			ParentId:       formatInt(ch.ParentID),
			MaxClients:     maxClients,
			Permanent:      ch.Type == 2,
			CurrentClients: currentClients,
		})
	}
	return resp
}

// warnInvalidChannelRow emits bounded, non-sensitive diagnostics for malformed
// backend rows. The value itself is not logged; only its channel ID and field
// name are useful to an operator.
func (c *controlService) warnInvalidChannelRow(channelID int64, field string, warnings *int) {
	if *warnings >= maxInvalidChannelWarnings {
		return
	}
	*warnings++
	if c.logger != nil {
		c.logger.Warn("skipping invalid channel row", zap.Int64("channel_id", channelID), zap.String("field", field))
	}
}

// uniqueChannels preserves first-occurrence order and treats a duplicate
// channel ID as malformed backend data whose later row is ignored. This keeps
// traversal and the wire response deterministic.
func uniqueChannels(all []query.ChannelInfo) []query.ChannelInfo {
	seen := make(map[int64]struct{}, len(all))
	unique := make([]query.ChannelInfo, 0, len(all))
	for _, channel := range all {
		if _, duplicate := seen[channel.ChannelID]; duplicate {
			continue
		}
		seen[channel.ChannelID] = struct{}{}
		unique = append(unique, channel)
	}
	return unique
}

// subtree returns the ids reachable from root (0 = the whole tree). Input
// order is not significant: rows may be returned child-before-parent. The
// first occurrence of a duplicate channel ID wins.
func subtree(all []query.ChannelInfo, root int64) map[int64]bool {
	return subtreeCanonical(uniqueChannels(all), root)
}

func subtreeCanonical(all []query.ChannelInfo, root int64) map[int64]bool {
	keep := make(map[int64]bool, len(all))
	if root == 0 {
		for _, ch := range all {
			keep[ch.ChannelID] = true
		}
		return keep
	}
	children := make(map[int64][]int64, len(all))
	foundRoot := false
	for _, ch := range all {
		children[ch.ParentID] = append(children[ch.ParentID], ch.ChannelID)
		if ch.ChannelID == root {
			foundRoot = true
		}
	}
	if !foundRoot {
		return keep
	}
	stack := []int64{root}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if keep[id] {
			continue
		}
		keep[id] = true
		stack = append(stack, children[id]...)
	}
	return keep
}
