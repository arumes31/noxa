package server

import (
	"context"
	"regexp"
	"slices"
	"strconv"

	"noxa/internal/authorization"
)

var roleMentionToken = regexp.MustCompile(`<@&([1-9][0-9]{0,18})>`)

// parseRoleMentions resolves identities under the same policy lease as the
// message write. Voice-channel membership is not a requirement to read chat.
func (s *TCPServer) parseRoleMentions(ctx context.Context, sender *Client, channelID int64, body string) []string {
	lease, ok := ctx.Value(roleLeaseKey{}).(roleLease)
	if !ok || s.deps == nil || s.deps.State == nil || lease.authority != s.deps.Authority || lease.evaluator == nil || sender.sessionRevoked() {
		return nil
	}
	e := lease.evaluator
	policy := e.Policy()
	mass := e.Evaluate(sender.userID(), channelID, authorization.MentionEveryone).Allowed
	requested := make(map[int64]bool)
	for _, token := range roleMentionToken.FindAllStringSubmatch(body, -1) {
		id, err := strconv.ParseInt(token[1], 10, 64)
		if err == nil && id != policy.EveryoneID {
			requested[id] = true
		}
	}
	allowed := make(map[int64]bool)
	for _, role := range policy.Roles {
		if requested[role.ID] && (role.Mentionable || mass) {
			allowed[role.ID] = true
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	members := make(map[int64]bool)
	for _, member := range policy.Members {
		for _, roleID := range member.RoleIDs {
			if allowed[roleID] {
				members[member.UserID] = true
				break
			}
		}
	}
	showInvisible := e.Evaluate(sender.userID(), 0, authorization.ViewConnectionInfo).Allowed
	seen := make(map[string]bool)
	var result []string
	for _, member := range s.deps.State.ListClients() {
		if !members[member.UserID] || member.UniqueID == "" || member.UniqueID == sender.UniqueID || seen[member.UniqueID] {
			continue
		}
		if !e.Evaluate(member.UserID, channelID, authorization.ViewChannel).Allowed ||
			!e.Evaluate(sender.userID(), member.ChannelID, authorization.ViewChannel).Allowed ||
			(member.Status == "invisible" && !showInvisible) {
			continue
		}
		seen[member.UniqueID] = true
		result = append(result, member.UniqueID)
	}
	slices.Sort(result)
	return result
}
