package server

import "strings"

// publicSessionRunnerFailureMessage separates diagnostic failures from the
// terminal message shown in a conversation. Audit and tool receipts keep the
// exact root cause; the conversation must not expose provider payloads, paths,
// runtime ownership terms, persistence internals, or construction language.
func publicSessionRunnerFailureMessage(raw, language string) string {
	message := strings.TrimSpace(raw)
	if message == "" {
		return localizedSessionRunnerFailureMessage(language, "generic")
	}
	normalized := strings.ToLower(message)
	if !sessionRunnerModelProviderUnavailableFailure(message) {
		return localizedSessionRunnerFailureMessage(language, "execution")
	}

	switch {
	case containsSessionRunnerFailureHint(normalized,
		"429", "too many requests", "rate limit", "rate_limit", "rate-limit",
		"quota", "usage limit", "resource exhausted"):
		return localizedSessionRunnerFailureMessage(language, "quota")
	case containsSessionRunnerFailureHint(normalized,
		"401", "403", "unauthorized", "forbidden", "invalid api key", "authentication"):
		return localizedSessionRunnerFailureMessage(language, "authentication")
	case containsSessionRunnerFailureHint(normalized,
		"404", "model_not_found", "model not found"):
		return localizedSessionRunnerFailureMessage(language, "model")
	case containsSessionRunnerFailureHint(normalized,
		"timeout", "deadline exceeded", "context deadline exceeded"):
		return localizedSessionRunnerFailureMessage(language, "timeout")
	case containsSessionRunnerFailureHint(normalized,
		"connection refused", "no such host", "dial tcp", "network"):
		return localizedSessionRunnerFailureMessage(language, "connection")
	default:
		return localizedSessionRunnerFailureMessage(language, "provider")
	}
}

func containsSessionRunnerFailureHint(message string, hints ...string) bool {
	for _, hint := range hints {
		if strings.Contains(message, hint) {
			return true
		}
	}
	return false
}

func localizedSessionRunnerFailureMessage(language, kind string) string {
	if strings.EqualFold(strings.TrimSpace(language), "zh") {
		switch kind {
		case "execution":
			return "任务本次运行未能完整结束。当前进度和已有结果均已保存；继续运行会从已保存的位置恢复。"
		case "quota":
			return "当前模型服务已达到额度或速率限制。任务、检查点、失败记录和已生成结果均已保留；切换到其他已配置模型后会从原任务继续。"
		case "authentication":
			return "当前模型服务认证失败。任务、检查点和已生成结果均已保留；修正凭据或切换模型后会从原任务继续。"
		case "model":
			return "当前模型在服务端不可用。任务、检查点和已生成结果均已保留；切换到其他已配置模型后会从原任务继续。"
		case "timeout":
			return "模型服务响应超时。任务、检查点和已生成结果均已保留；稍后继续或切换模型会从原任务恢复。"
		case "connection":
			return "暂时无法连接当前模型服务。任务、检查点和已生成结果均已保留；端点恢复或切换模型后会从原任务继续。"
		default:
			return "当前模型服务暂时不可用。任务、检查点和已生成结果均已保留；稍后继续或切换模型不会重建任务。"
		}
	}

	switch kind {
	case "execution":
		return "This run did not finish completely. Current progress and existing results were preserved; continuing resumes from the saved position."
	case "quota":
		return "The current model service reached its quota or rate limit. The task, checkpoints, failure record, and generated results were preserved; switching models resumes the same task."
	case "authentication":
		return "Authentication for the current model service failed. The task, checkpoints, and generated results were preserved; fix the credentials or switch models to resume the same task."
	case "model":
		return "The current model is unavailable at the provider. The task, checkpoints, and generated results were preserved; switch models to resume the same task."
	case "timeout":
		return "The model service timed out. The task, checkpoints, and generated results were preserved; continue later or switch models to resume the same task."
	case "connection":
		return "The current model service could not be reached. The task, checkpoints, and generated results were preserved; restore the endpoint or switch models to resume the same task."
	default:
		return "The current model service is temporarily unavailable. The task, checkpoints, and generated results were preserved; continuing later or switching models will not recreate the task."
	}
}
