package provider

import (
	"strings"
	"testing"

	"github.com/reuben/go-harness/internal/types"
)

func userMsg(text string) *types.Message {
	return &types.Message{Role: types.UserRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock(text)}}
}

func toolCallMsg(callIDs ...string) *types.Message {
	blocks := make([]types.ContentBlock, 0, len(callIDs))
	for _, id := range callIDs {
		blocks = append(blocks, types.ContentBlock{
			Kind: types.ToolBlock, ToolCallID: id, ToolName: "bash", Args: `{"command":"ls"}`,
		})
	}
	return &types.Message{Role: types.AssistantRole, ContentBlocks: blocks, StopReason: types.ToolUserStop}
}

func toolResultMsg(callID string) *types.Message {
	return &types.Message{
		Role: types.ToolResultRole, ToolCallID: callID,
		ContentBlocks: []types.ContentBlock{types.NewTextBlock("结果")},
	}
}

// 每条 assistant 的 tool_call 都要有配对的 tool 消息；缺的补合成的，
// 位置在打断它的那条消息之前
func TestNormalizeToolCallsPairsOrphans(t *testing.T) {
	got := NormalizeToolCalls([]*types.Message{
		userMsg("跑一下"),
		toolCallMsg("c1", "c2"),
		toolResultMsg("c1"), // c2 的结果缺了
		userMsg("继续"),
	})

	if len(got) != 5 {
		t.Fatalf("该补出一条合成的 tool_result，实际 %d 条消息", len(got))
	}
	synthetic := got[3]
	if synthetic.Role != types.ToolResultRole || synthetic.ToolCallID != "c2" {
		t.Fatalf("第 4 条该是给 c2 补的结果，实际 %+v", synthetic)
	}
	if !synthetic.IsError || !strings.Contains(synthetic.Text(), "bash") {
		t.Errorf("合成结果要说清楚哪个工具没返回：%+v", synthetic)
	}
	// 补在「继续」之前，不是攒到末尾
	if got[4].Role != types.UserRole {
		t.Errorf("补出来的结果该插在打断它的消息前面，实际顺序 %v", rolesOf(got))
	}
}

// 历史停在没配对的调用上也一样补
func TestNormalizeToolCallsPairsTrailingOrphan(t *testing.T) {
	got := NormalizeToolCalls([]*types.Message{userMsg("跑一下"), toolCallMsg("c1")})

	if len(got) != 3 || got[2].Role != types.ToolResultRole || got[2].ToolCallID != "c1" {
		t.Fatalf("收尾的调用也该补，实际 %v", rolesOf(got))
	}
}

// 配对齐全的历史一条不多一条不少，顺序也不动
func TestNormalizeToolCallsLeavesPairedAlone(t *testing.T) {
	in := []*types.Message{
		userMsg("跑一下"),
		toolCallMsg("c1"),
		toolResultMsg("c1"),
		{Role: types.AssistantRole, ContentBlocks: []types.ContentBlock{types.NewTextBlock("答")}},
		userMsg("再问"),
	}
	got := NormalizeToolCalls(in)

	if len(got) != len(in) {
		t.Fatalf("配对齐全不该动它，实际 %d 条", len(got))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("第 %d 条被换掉了", i)
		}
	}
}

// 被中止的那一轮在 core 里已经补过配对，这里不该把 assistant 跳掉 ——
// 跳掉的话它后面那条配对的 tool_result 就成了孤儿
func TestNormalizeToolCallsKeepsAbortedTurn(t *testing.T) {
	aborted := toolCallMsg("c1")
	aborted.StopReason = types.AbortedStop
	aborted.ErrorMessage = "用户取消了这一轮"

	got := NormalizeToolCalls([]*types.Message{userMsg("跑一下"), aborted, toolResultMsg("c1")})

	if len(got) != 3 {
		t.Fatalf("配对是齐的，不该增删，实际 %v", rolesOf(got))
	}
}

func rolesOf(messages []*types.Message) []types.Role {
	roles := make([]types.Role, 0, len(messages))
	for _, msg := range messages {
		roles = append(roles, msg.Role)
	}
	return roles
}
