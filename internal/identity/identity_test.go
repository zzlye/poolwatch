package identity

import "testing"

func TestIdentifiersAreUniqueAndHashable(t *testing.T) {
	first, err := NewID("target")
	if err != nil {
		t.Fatalf("生成标识失败: %v", err)
	}
	second, err := NewID("target")
	if err != nil {
		t.Fatalf("生成标识失败: %v", err)
	}
	if first == second || len(first) != len("target_")+32 {
		t.Fatalf("随机标识不符合预期: %q, %q", first, second)
	}
	if HashToken("same") != HashToken("same") || HashToken("same") == HashToken("other") {
		t.Fatal("令牌摘要结果不符合预期")
	}
}
