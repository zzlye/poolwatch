package api

import (
	"poolwatch/internal/monitor"
	"testing"
)

// 新版管理令牌无需用户编号，旧版 Cookie 方式仍必须校验编号。
func TestNewAPIManagementTokenDraftAllowsEmptyUserID(t *testing.T) {
	credential, err := mergeNewAPICredential(monitor.Credential{}, targetDraft{AccessToken: "synthetic-management-token"}, credentialModeNewAPIAccessToken)
	if err != nil {
		t.Fatalf("管理令牌模式不应强制旧版用户编号：%v", err)
	}
	if credential.AccessToken == "" || credential.UserID != "" {
		t.Fatal("凭据合并不符合预期")
	}
	if _, err := mergeNewAPICredential(monitor.Credential{}, targetDraft{Cookie: "session=synthetic"}, credentialModeNewAPIBrowserSession); err == nil {
		t.Fatal("旧版 Cookie 模式仍需用户编号")
	}
	replaced, err := mergeNewAPICredential(monitor.Credential{AccessToken: "old-token", UserID: "42"}, targetDraft{AccessToken: "new-token"}, credentialModeNewAPIAccessToken)
	if err != nil || replaced.UserID != "" {
		t.Fatalf("更换新版管理令牌时应清除旧用户编号：%v", err)
	}
}
