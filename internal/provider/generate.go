package provider

import (
	"context"

	"github.com/reuben/go-harness/internal/tui"
	"github.com/reuben/go-harness/internal/types"
)

func GenerateFunc(ctx context.Context, conversation types.Conversation) <-chan types.AssistantEvent {

	return nil
}

func CallLLM(ctx context.Context, runContext *types.Conversation, generateFunc GenerateFunction, emit tui.EventSend) *types.Message {

	return nil
}

type GenerateFunction func(context.Context, types.Conversation) <-chan types.AssistantEvent
