package markets

import (
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

// DirLoader is the production DeploymentsLoader that reads from a
// directory on disk.
type DirLoader struct {
	dir string
}

// NewDirLoader resolves the deployments directory via
// deployments.DiscoverDir(): CONTRACTS_DEPLOYMENTS_DIR when set, else the
// repo-checkout packages/shared/deployments found by walking up from the
// working directory. When neither resolves the loader returns an empty
// map — matches the NestJS behaviour where a missing dir yields no
// markets (the service degrades to an empty list rather than crashing).
func NewDirLoader() *DirLoader {
	return &DirLoader{dir: deployments.DiscoverDir()}
}

// NewDirLoaderAt is the test/CLI variant that takes an explicit path.
func NewDirLoaderAt(dir string) *DirLoader {
	return &DirLoader{dir: dir}
}

// Load implements DeploymentsLoader.
func (l *DirLoader) Load() (map[int]deployments.ChainConfig, error) {
	return deployments.LoadMerged(l.dir)
}

// Dir reports the directory the loader was configured with — used in
// startup logs so operators can confirm the wiring.
func (l *DirLoader) Dir() string {
	return l.dir
}
