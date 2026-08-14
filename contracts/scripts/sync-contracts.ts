import { syncDeploymentArtifacts } from "./deployment-artifacts.js";

function main() {
  console.log("=== Sync Contract Deployment Artifacts ===\n");
  syncDeploymentArtifacts();
  console.log("✅ Synced deployment JSON files to packages/shared/deployments");
  console.log("✅ Regenerated packages/shared/src/web3/contract-addresses.generated.ts");
}

main();
