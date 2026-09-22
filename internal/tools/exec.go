package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/reuben/go-harness/internal/types"
)

func FailTruncatedToolCalls(callContentBlocks []types.ContentBlock, emit types.EventSend) ([]*types.Message, bool) {
	var toolResultMessages []*types.Message

	for _, b := range callContentBlocks {
		emit(types.Event{
			Type:       types.ToolStartEvent,
			ToolCallID: b.ToolCallID,
			ToolName:   b.ToolName,
		})

		toolResult := errorResult(fmt.Sprintf(
			"工具 %s 没有执行：模型输出撞到了 token 上限，这次调用的参数可能是截断的。请重新发起这次调用。",
			b.ToolName,
		))

		emit(types.Event{
			Type:            types.ToolEndEvent,
			ToolCallID:      b.ToolCallID,
			ToolName:        b.ToolName,
			ToolFinalResult: &toolResult,
			IsError:         true,
		})

		msg := toolResultMessage(b, toolResult, true)
		emit(types.Event{
			Type:    types.MessageStartEvent,
			Message: msg,
		})
		emit(types.Event{
			Type:    types.MessageEndEvent,
			Message: msg,
		})

		toolResultMessages = append(toolResultMessages, msg)
	}

	return toolResultMessages, false
}

func ExecToolCalls(ctx context.Context, toolset []*types.Tool, callContentBlocks []types.ContentBlock, emit types.EventSend) ([]*types.Message, bool) {
	var toolResultMessages []*types.Message
	toolResults := make([]types.ToolResult, 0, len(callContentBlocks))

	for _, b := range callContentBlocks {
		toolResult, isError := execToolCall(ctx, toolset, b, emit)
		toolResults = append(toolResults, toolResult)
		msg := toolResultMessage(b, toolResult, isError)
		emit(types.Event{
			Type:    types.MessageStartEvent,
			Message: msg,
		})
		emit(types.Event{
			Type:    types.MessageEndEvent,
			Message: msg,
		})
		toolResultMessages = append(toolResultMessages, msg)
	}

	return toolResultMessages, resolveShouldStop(toolResults)
}

func resolveShouldStop(toolResults []types.ToolResult) bool {
	for _, r := range toolResults {
		if !r.ShouldStop {
			return false
		}
	}
	return true
}

func execToolCall(ctx context.Context, toolset []*types.Tool, callContentBlock types.ContentBlock, emit types.EventSend) (types.ToolResult, bool) {
	args, err := parseToolCallArgs(callContentBlock.Args)
	emit(types.Event{
		Type:       types.ToolStartEvent,
		ToolCallID: callContentBlock.ToolCallID,
		ToolName:   callContentBlock.ToolName,
		Args:       args,
	})

	var toolResult types.ToolResult
	isError := false
	if err != nil {
		toolResult, isError = errorResult(
			fmt.Sprintf(
				"工具 %s 的参数不是合法的 JSON：%v",
				callContentBlock.ToolName,
				err,
			),
		), true
	} else {
		toolResult, isError = runTool(ctx, toolset, callContentBlock, emit, args)
	}

	emit(types.Event{
		Type:            types.ToolEndEvent,
		ToolCallID:      callContentBlock.ToolCallID,
		ToolName:        callContentBlock.ToolName,
		Args:            args,
		ToolFinalResult: &toolResult,
		IsError:         isError,
	})

	return toolResult, isError
}

func runTool(ctx context.Context, toolset []*types.Tool, callContentBlock types.ContentBlock, emit types.EventSend, args map[string]any) (types.ToolResult, bool) {
	tool := findTool(toolset, callContentBlock.ToolName)
	if tool == nil {
		return errorResult(fmt.Sprintf(
			"没有叫 %s 的工具",
			callContentBlock.ToolName,
		)), true
	}
	if err := ctx.Err(); err != nil {
		return errorResult("已取消：" + err.Error()), true
	}

	var mu sync.Mutex
	settled := false
	partialSend := func(partialResult types.ToolResult) {
		mu.Lock()
		defer mu.Unlock()
		if settled {
			return
		}
		emit(types.Event{
			Type:              types.ToolPartialEvent,
			ToolCallID:        callContentBlock.ToolCallID,
			ToolName:          callContentBlock.ToolName,
			Args:              args,
			ToolPartialResult: &partialResult,
		})
	}

	toolResult, err := tool.Execute(ctx, args, partialSend)

	mu.Lock()
	settled = true
	mu.Unlock()

	if err != nil {
		return errorResult(err.Error()), true
	}

	return toolResult, false
}

func findTool(toolset []*types.Tool, name string) *types.Tool {
	for _, tool := range toolset {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

func parseToolCallArgs(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}

func errorResult(text string) types.ToolResult {
	return types.ToolResult{ContentBlock: []types.ContentBlock{types.NewTextBlock(text)}}
}

func toolResultMessage(callContentBlock types.ContentBlock, toolResult types.ToolResult, isError bool) *types.Message {
	return &types.Message{
		Role:          types.ToolResultRole,
		ContentBlocks: toolResult.ContentBlock,
		ToolCallID:    callContentBlock.ToolCallID,
		ToolName:      callContentBlock.ToolName,
		IsError:       isError,
	}
}
