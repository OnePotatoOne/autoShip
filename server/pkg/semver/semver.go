// Package semver 提供语义化版本号（vX.Y.Z）解析、大小比较、升级防降级检查及发布版本号自动递增计算。
package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version 表示解析后的三段式语义化版本号（Major.Minor.Patch 及可选预发布后缀）。
type Version struct {
	Major      int    // 主版本号
	Minor      int    // 次版本号
	Patch      int    // 修订号
	PreRelease string // 可选预发布标签（如 rc.1），正式版为空字符串
}

var semverPattern = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?$`)

// Parse 解析形如 "v1.2.3" 或 "1.2.3" 的语义化版本字符串。
// 参数 raw 为版本号文本；返回解析后的 Version 结构体及格式错误，通过 semver.Parse("v1.2.0") 调用。
func Parse(raw string) (Version, error) {
	matches := semverPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if matches == nil {
		return Version{}, fmt.Errorf("无效的语义化版本号: %q", raw)
	}
	maj, _ := strconv.Atoi(matches[1])
	min, _ := strconv.Atoi(matches[2])
	pat, _ := strconv.Atoi(matches[3])
	return Version{
		Major:      maj,
		Minor:      min,
		Patch:      pat,
		PreRelease: matches[4],
	}, nil
}

// String 返回统一带 "v" 前缀的标准版本号文本（如 "v1.2.3"）。
// 无入参；返回格式化后的版本号字符串。
func (v Version) String() string {
	if v.PreRelease != "" {
		return fmt.Sprintf("v%d.%d.%d-%s", v.Major, v.Minor, v.Patch, v.PreRelease)
	}
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare 比较当前版本 v 与另一版本 other 的先后顺序。
// 参数 other 为待比较版本；v < other 返回 -1，v == other 返回 0，v > other 返回 1。
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}
		return 1
	}
	// 正式版高于同数字的预发布版
	if v.PreRelease == "" && other.PreRelease != "" {
		return 1
	}
	if v.PreRelease != "" && other.PreRelease == "" {
		return -1
	}
	return strings.Compare(v.PreRelease, other.PreRelease)
}

// IsUpgrade 判断候选版本 candidate 是否严格高于当前已运行版本 current（用于 9.4 节升级流程防降级校验；首次安装 current 为空时允许任意合法版本）。
// 参数 current 为当前版本字符串（空串表示全新部署），candidate 为待部署版本字符串；返回是否允许升级及解析错误。
func IsUpgrade(current, candidate string) (bool, error) {
	candVer, err := Parse(candidate)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(current) == "" || current == "0.0.0" || current == "v0.0.0" {
		return true, nil
	}
	curVer, err := Parse(current)
	if err != nil {
		return false, err
	}
	return candVer.Compare(curVer) > 0, nil
}

// Next 计算下一个发布版本号（遵循 8.10 节规则：含新功能升次版本号并归零修订号，纯修复升修订号；无历史版本时从 v0.1.0 起步）。
// 参数 latest 为当前最新版本号（可为空），hasFeature 表示本次版本是否包含新功能/改进类需求；返回带 "v" 前缀的新版本号及解析错误。
func Next(latest string, hasFeature bool) (string, error) {
	if strings.TrimSpace(latest) == "" {
		return "v0.1.0", nil
	}
	cur, err := Parse(latest)
	if err != nil {
		return "", err
	}
	if hasFeature {
		return Version{Major: cur.Major, Minor: cur.Minor + 1, Patch: 0}.String(), nil
	}
	return Version{Major: cur.Major, Minor: cur.Minor, Patch: cur.Patch + 1}.String(), nil
}
