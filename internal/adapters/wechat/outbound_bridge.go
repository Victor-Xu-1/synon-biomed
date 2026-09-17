package wechat

import (
	"context"
	"errors"

	adaptercommon "synon-go/internal/adapters/common"
)

func RegisterOutboundBridge(bridge *adaptercommon.WsBridge, chatKey string, targetUserID string, processor *OutboundProcessor, options adaptercommon.OutboundBridgeOptions) {
	adaptercommon.RegisterOutboundHandler(bridge, chatKey, func(ctx context.Context, message adaptercommon.ServerMessage) error {
		if processor == nil {
			return errors.New("wechat outbound processor is required")
		}
		report, err := processor.ProcessServerMessageWithReport(ctx, targetUserID, message)
		if options.OnReport != nil {
			options.OnReport(report)
		}
		return err
	}, options)
}
