package config

import (
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// GitOpsConfig deliberately has no credentials, insecure-TLS, or local-repo
// fields. The initial contract is an anonymously readable HTTPS repository.
type GitOpsConfig struct {
	RepoURL  string                    `yaml:"repoURL"`
	Revision string                    `yaml:"revision"`
	RootPath string                    `yaml:"rootPath"`
	Identity *GitOpsRepositoryIdentity `yaml:"identity,omitempty"`
}

// Identity is descriptive only and grants no repository or cluster authority.
type GitOpsRepositoryIdentity struct {
	Owner string `yaml:"owner"`
	Name  string `yaml:"name"`
}

var (
	gitRepositorySegment  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	gitRootSegment        = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
	gitRevisionCharacters = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}$`)
)

// ValidateGitOps requires the GitOps contract without coupling offline GitOps
// rendering to SSH keys or a currently running Kubernetes cluster.
func (c Config) ValidateGitOps() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Spec.GitOps == nil {
		return &ValidationError{Problems: []string{"spec.gitops is required for GitOps operations"}}
	}
	return nil
}

func validateOptionalGitOps(settings *GitOpsConfig) []string {
	if settings == nil {
		return nil
	}
	var problems []string
	if !validGitRepositoryURL(settings.RepoURL) {
		problems = append(problems, "spec.gitops.repoURL must be a canonical public HTTPS Git URL without credentials, query, or fragment")
	}
	if !validGitRevision(settings.Revision) {
		problems = append(problems, "spec.gitops.revision must be an explicit safe branch, tag, or commit revision")
	}
	if !validGitRootPath(settings.RootPath) {
		problems = append(problems, "spec.gitops.rootPath must be a canonical portable repository-relative directory outside Bareplane, Git, and generated state paths")
	}
	if settings.Identity != nil {
		if !validRepositoryOwner(settings.Identity.Owner) || !gitRepositorySegment.MatchString(settings.Identity.Name) || strings.HasSuffix(strings.ToLower(settings.Identity.Name), ".lock") {
			problems = append(problems, "spec.gitops.identity must contain a safe repository owner and name; metadata does not provide authentication")
		}
	}
	return problems
}

func validGitRepositoryURL(value string) bool {
	if len(value) > 2048 || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\\#\x00\r\n\t ") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || strings.HasSuffix(parsed.Host, ":") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if address, err := netip.ParseAddr(host); err == nil {
		if !address.IsGlobalUnicast() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.Is4In6() || address.Zone() != "" {
			return false
		}
	} else if !validDomain(host) || !strings.Contains(host, ".") || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.ParseUint(port, 10, 16)
		if err != nil || value == 0 || strconv.FormatUint(value, 10) != port {
			return false
		}
	}
	if parsed.Path == "" || parsed.Path == "/" || path.Clean(parsed.Path) != parsed.Path || strings.HasSuffix(parsed.Path, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/") {
		if !gitRepositorySegment.MatchString(part) {
			return false
		}
	}
	return true
}

func validRepositoryOwner(owner string) bool {
	if len(owner) == 0 || len(owner) > 255 {
		return false
	}
	for _, part := range strings.Split(owner, "/") {
		if !gitRepositorySegment.MatchString(part) {
			return false
		}
	}
	return true
}

func validGitRevision(revision string) bool {
	if !gitRevisionCharacters.MatchString(revision) || strings.Contains(revision, "..") || strings.Contains(revision, "//") || strings.HasSuffix(revision, "/") || strings.HasSuffix(revision, ".") {
		return false
	}
	for _, part := range strings.Split(revision, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(strings.ToLower(part), ".lock") {
			return false
		}
	}
	return true
}

func protectedGitPath(part string) bool {
	switch strings.ToLower(part) {
	case ".", "..", ".git", ".bareplane", "terraform", "state", "node_modules":
		return true
	}
	return false
}

func validGitRootPath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || path.Clean(value) != value || strings.HasSuffix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if !gitRootSegment.MatchString(part) || protectedGitPath(part) || strings.HasSuffix(part, ".") {
			return false
		}
		stem := strings.SplitN(part, ".", 2)[0]
		if stem == "con" || stem == "prn" || stem == "aux" || stem == "nul" || (len(stem) == 4 && (strings.HasPrefix(stem, "com") || strings.HasPrefix(stem, "lpt")) && stem[3] >= '0' && stem[3] <= '9') {
			return false
		}
	}
	return true
}
