import assert from "node:assert/strict";
import { promises as fs } from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { verifyContract } from "./verify-frontend-rpc-contract.mjs";

async function fixture(t, methods, source) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "komari-rpc-contract-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const sourceRoot = path.join(root, "src");
  await fs.mkdir(sourceRoot);
  const contractPath = path.join(root, "contract.json");
  await fs.writeFile(
    contractPath,
    JSON.stringify({ contract: "test.v1", required_methods: methods }),
  );
  await fs.writeFile(path.join(sourceRoot, "client.tsx"), source);
  return { contractPath, sourceRoot };
}

test("accepts every RPC literal declared by the contract", async (t) => {
  const paths = await fixture(
    t,
    ["common:getNodes", "rpc.ping"],
    `call("common:getNodes"); call('rpc.ping'); const ignored = "common.title";`,
  );
  const result = await verifyContract(paths.contractPath, paths.sourceRoot);
  assert.deepEqual(result.referenced, ["common:getNodes", "rpc.ping"]);
});

test("fails closed when the frontend adds an undeclared method", async (t) => {
  const paths = await fixture(t, ["common:getNodes"], `call("public:newFeature")`);
  await assert.rejects(
    verifyContract(paths.contractPath, paths.sourceRoot),
    /public:newFeature/,
  );
});
