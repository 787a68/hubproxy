package utils

import (
	"strings"

	"hubproxy/config"
)

// AccessController 统一访问控制器
type AccessController struct {
}

// dockerImageInfo Docker镜像信息
type dockerImageInfo struct {
	Namespace  string
	Repository string
	Tag        string
	FullName   string
}

// GlobalAccessController 全局访问控制器实例
var GlobalAccessController = &AccessController{}

// parseDockerImage 解析Docker镜像名称
func (ac *AccessController) parseDockerImage(image string) dockerImageInfo {
	image = strings.TrimPrefix(image, "docker://")

	var tag string
	if idx := strings.LastIndex(image, ":"); idx != -1 {
		part := image[idx+1:]
		// 端口冒号后的部分含 "/" 则不是 tag（如 registry:5000/myimage）
		if !strings.Contains(part, "/") {
			tag = part
			image = image[:idx]
		}
	}
	if tag == "" {
		tag = "latest"
	}

	var namespace, repository string
	if strings.Contains(image, "/") {
		parts := strings.Split(image, "/")
		if len(parts) >= 2 {
			// parts[0] 含 "." 或 ":" 视为 registry 域名
			if strings.ContainsAny(parts[0], ".:") {
				if len(parts) >= 3 {
					namespace = parts[1]
					repository = parts[2]
				} else {
					// registry/user 形式，user 既是 namespace 也是 repository
					namespace = parts[1]
					repository = parts[1]
				}
			} else {
				namespace = parts[0]
				repository = parts[1]
			}
		}
	} else {
		namespace = "library"
		repository = image
	}

	fullName := namespace + "/" + repository

	return dockerImageInfo{
		Namespace:  namespace,
		Repository: repository,
		Tag:        tag,
		FullName:   fullName,
	}
}

// CheckDockerAccess 检查Docker镜像访问权限
func (ac *AccessController) CheckDockerAccess(image string) (allowed bool, reason string) {
	cfg := config.GetConfig()

	imageInfo := ac.parseDockerImage(image)

	if len(cfg.Access.WhiteList) > 0 {
		if !matchWildcard(imageInfo.FullName, imageInfo.Namespace, imageInfo.Repository, cfg.Access.WhiteList) {
			return false, "不在Docker镜像白名单内"
		}
	}

	if len(cfg.Access.BlackList) > 0 {
		if matchWildcard(imageInfo.FullName, imageInfo.Namespace, imageInfo.Repository, cfg.Access.BlackList) {
			return false, "Docker镜像在黑名单内"
		}
	}

	return true, ""
}

// CheckGitHubAccess 检查GitHub仓库访问权限
func (ac *AccessController) CheckGitHubAccess(matches []string) (allowed bool, reason string) {
	if len(matches) < 2 {
		return false, "无效的GitHub仓库格式"
	}

	cfg := config.GetConfig()

	username := strings.ToLower(strings.TrimSpace(matches[0]))
	repoName := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(matches[1], ".git")))
	fullRepo := username + "/" + repoName

	if len(cfg.Access.WhiteList) > 0 && !matchWildcard(fullRepo, username, repoName, cfg.Access.WhiteList) {
		return false, "不在GitHub仓库白名单内"
	}

	if len(cfg.Access.BlackList) > 0 && matchWildcard(fullRepo, username, repoName, cfg.Access.BlackList) {
		return false, "GitHub仓库在黑名单内"
	}

	return true, ""
}

// matchWildcard 通配符匹配，统一用于 Docker 镜像和 GitHub 仓库的黑白名单检查
// fullName: "namespace/repository" 或 "user/repo"
// namespace: namespace 或 user
// repository: repository 或 repo
// list: 规则列表，支持: 完全匹配、ns/*、ns/repo、*/repo、prefix*、ns/prefix*
func matchWildcard(fullName, namespace, repository string, list []string) bool {
	fullName = strings.ToLower(fullName)
	namespace = strings.ToLower(namespace)
	repository = strings.ToLower(repository)

	for _, item := range list {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}

		// 完全匹配
		if fullName == item {
			return true
		}

		// namespace 或 namespace/* 匹配
		if item == namespace || item == namespace+"/*" {
			return true
		}

		// 后缀通配: prefix*
		if strings.HasSuffix(item, "*") {
			prefix := strings.TrimSuffix(item, "*")
			if strings.HasPrefix(fullName, prefix) {
				return true
			}
		}

		// 前缀目录匹配: ns/...
		if strings.HasPrefix(fullName, item+"/") {
			return true
		}

		// 前缀通配: */repo 或 */prefix*
		if strings.HasPrefix(item, "*/") {
			p := item[2:]
			if p == repository {
				return true
			}
			if strings.HasSuffix(p, "*") && strings.HasPrefix(repository, p[:len(p)-1]) {
				return true
			}
		}
	}
	return false
}
