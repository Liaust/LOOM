package serviceregistry

import "strings"

// Project source identity is real producer ownership, not invented repository membership.
func validApplicationSourceIdentity(repository, source string) bool {
	if repository != "" {
		return source == "" && applicationRepoPattern.MatchString(repository)
	}
	parts := strings.Split(source, "/")
	return len(parts) == 2 && applicationProjectPattern.MatchString(parts[0]) && applicationKeyPattern.MatchString(parts[1])
}

func applicationSourceIdentity(repository, source string) string {
	if source != "" {
		return source
	}
	return repository
}

func (d ApplicationArtifactDescriptor) ProducerIdentity() string {
	return applicationSourceIdentity(d.RepositoryID, d.SourceIdentity)
}
func (g ApplicationGrant) ProducerIdentity() string {
	return applicationSourceIdentity(g.RepositoryID, g.SourceIdentity)
}
func (q ApplicationProvisionRequest) ProducerIdentity() string {
	return applicationSourceIdentity(q.RepositoryID, q.SourceIdentity)
}
func (p ApplicationPrerequisiteSnapshot) ProducerIdentity() string {
	return applicationSourceIdentity(p.RepositoryID, p.SourceIdentity)
}
func (p ApplicationPrerequisiteArtifact) ProducerIdentity() string {
	return applicationSourceIdentity(p.RepositoryID, p.SourceIdentity)
}
