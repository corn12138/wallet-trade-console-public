import * as path from "node:path";
import { fileURLToPath } from "node:url";

export function getScriptDir(metaUrl: string): string {
  return path.dirname(fileURLToPath(metaUrl));
}
