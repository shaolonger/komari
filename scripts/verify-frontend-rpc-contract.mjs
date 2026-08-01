#!/usr/bin/env node

import { promises as fs } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const rpcLiteral = /["'`](?:admin|public|common|client):[A-Za-z][A-Za-z0-9]*["'`]|["'`]rpc\.[A-Za-z][A-Za-z0-9.]*["'`]/g;

export async function sourceFiles(root) {
  const result = [];
  async function walk(directory) {
    const entries = await fs.readdir(directory, { withFileTypes: true });
    for (const entry of entries) {
      const target = path.join(directory, entry.name);
      if (entry.isDirectory()) {
        if (!["node_modules", "dist", ".git"].includes(entry.name)) await walk(target);
      } else if (/\.(?:ts|tsx|js|jsx|mjs)$/.test(entry.name)) {
        result.push(target);
      }
    }
  }
  await walk(root);
  return result.sort();
}

export async function referencedMethods(sourceRoot) {
  const methods = new Set();
  for (const file of await sourceFiles(sourceRoot)) {
    const content = await fs.readFile(file, "utf8");
    for (const literal of content.match(rpcLiteral) ?? []) methods.add(literal.slice(1, -1));
  }
  return [...methods].sort();
}

export async function verifyContract(contractPath, sourceRoot) {
  const contract = JSON.parse(await fs.readFile(contractPath, "utf8"));
  if (!Array.isArray(contract.required_methods) || typeof contract.contract !== "string") {
    throw new Error("invalid RPC contract: required_methods and contract are mandatory");
  }
  const available = new Set(contract.required_methods);
  const referenced = await referencedMethods(sourceRoot);
  const missing = referenced.filter((method) => !available.has(method));
  if (missing.length > 0) {
    throw new Error(
      `frontend references RPC methods absent from ${contract.contract}: ${missing.join(", ")}`,
    );
  }
  return { contract: contract.contract, referenced };
}

async function main() {
  const [, , contractPath, sourceRoot] = process.argv;
  if (!contractPath || !sourceRoot) {
    throw new Error("usage: verify-frontend-rpc-contract.mjs CONTRACT_JSON FRONTEND_SOURCE");
  }
  const result = await verifyContract(contractPath, sourceRoot);
  process.stdout.write(
    `verified ${result.referenced.length} frontend RPC methods against ${result.contract}\n`,
  );
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
