// Package semver_test 验证语义化版本号比较、防降级检查及自动升版计算。
package semver_test

import (
	"testing"

	"autoship/pkg/semver"
)

// TestSemverUpgradeAndNext 验证防降级拦截与新功能升次版本、纯修复升修订号规则。
func TestSemverUpgradeAndNext(t *testing.T) {
	ok, err := semver.IsUpgrade("v1.2.3", "v1.2.4")
	if err != nil || !ok {
		t.Fatalf("正常升级被误拦: %v", err)
	}
	okDown, err := semver.IsUpgrade("v1.2.3", "v1.2.2")
	if err != nil || okDown {
		t.Fatalf("降级版本未被拦截")
	}
	okSame, err := semver.IsUpgrade("v1.2.3", "1.2.3")
	if err != nil || okSame {
		t.Fatalf("同版本号重复升级未被拦截")
	}

	nextFeat, err := semver.Next("v1.2.3", true)
	if err != nil || nextFeat != "v1.3.0" {
		t.Fatalf("新功能升版期望 v1.3.0，实际 %s (%v)", nextFeat, err)
	}
	nextFix, err := semver.Next("v1.2.3", false)
	if err != nil || nextFix != "v1.2.4" {
		t.Fatalf("修复升版期望 v1.2.4，实际 %s (%v)", nextFix, err)
	}
}
